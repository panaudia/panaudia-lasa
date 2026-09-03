package engine

import (
	"encoding/binary"
	"math"
	"testing"
)

func rawSamples(freq float64, frame int) []byte {
	return rawFrame(freq, frame)
}

func decodeRawBytes(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

func rms(x []float32) float64 {
	var acc float64
	for _, v := range x {
		acc += float64(v) * float64(v)
	}
	return math.Sqrt(acc / float64(len(x)))
}

// dominant returns the strongest bin's frequency in Hz by a coarse DFT
// over the candidate list.
func dominant(x []float32, candidates []float64) float64 {
	best, bestMag := 0.0, -1.0
	for _, f := range candidates {
		var re, im float64
		for i, v := range x {
			ph := 2 * math.Pi * f * float64(i) / SampleRate
			re += float64(v) * math.Cos(ph)
			im -= float64(v) * math.Sin(ph)
		}
		if m := re*re + im*im; m > bestMag {
			best, bestMag = f, m
		}
	}
	return best
}

// TestSourceGroupChannels pins the lockstep audio path on the raw
// codec: two members share one buffer, each reads back its own channel
// in step, a lost channel is silence while its sibling plays, poses
// align per member, and the set is freed with its last member.
func TestSourceGroupChannels(t *testing.T) {
	m, err := New(Config{Order: 2, MaxEntities: 4, ReverbPreset: ReverbNone, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	spec := func(i int) *GroupSpec { return &GroupSpec{ID: "c1", Index: i, Count: 2} }
	a, err := m.AddSource("a", SourceConfig{Codec: SourceCodecRawF32, Group: spec(0), InitialPose: Pose{X: 1}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.AddSource("b", SourceConfig{Codec: SourceCodecRawF32, Group: spec(1), InitialPose: Pose{X: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Group() == nil || a.Group() != b.Group() || a.Group().Count() != 2 {
		t.Fatal("members do not share one group")
	}
	if _, err := m.AddSource("c", SourceConfig{Codec: SourceCodecRawF32, Group: &GroupSpec{ID: "c1", Index: 2, Count: 2}}); err != ErrBadGroup {
		t.Fatalf("index outside the set: %v, want ErrBadGroup", err)
	}
	g := a.Group()

	// Prime and then run: channel 0 carries 440 Hz, channel 1 carries
	// 1000 Hz, with per-member poses that walk with seq.
	write := func(seq uint64, lostB bool) {
		chans := []GroupChannel{
			{Pose: &Pose{X: float64(seq)}, Audio: rawSamples(440, int(seq))},
			{Pose: &Pose{X: -float64(seq)}, Audio: rawSamples(1000, int(seq))},
		}
		if lostB {
			chans[1] = GroupChannel{Lost: true, AudioLikely: true}
		}
		g.WriteFrame(seq, chans)
	}
	seq := uint64(0)
	for ; seq < 20; seq++ {
		write(seq, false)
	}
	fa, fb := make([]float32, FrameSize), make([]float32, FrameSize)
	var lastA, lastB Pose
	for i := 0; i < 100; i++ {
		write(seq, false)
		seq++
		m.Process()
		// Read the members the way phaseIn does, after prep's group read.
		lastA, _ = a.readFrame(fa)
		lastB, _ = b.readFrame(fb)
	}
	// The group read already happened inside Process; a second read
	// per tick here takes the next frame, which is fine for a check of
	// channel identity.
	cands := []float64{440, 1000}
	if rms(fa) < 0.05 || rms(fb) < 0.05 {
		t.Fatalf("members read silence: rms a %.3f b %.3f", rms(fa), rms(fb))
	}
	if dominant(fa, cands) != 440 || dominant(fb, cands) != 1000 {
		t.Fatalf("channels crossed: a=%v b=%v", dominant(fa, cands), dominant(fb, cands))
	}
	if !(lastA.X > 0 && lastB.X < 0) {
		t.Fatalf("poses not per member: a %.1f b %.1f", lastA.X, lastB.X)
	}
	if a.LatencySamples() != b.LatencySamples() || a.LatencySamples() == 0 {
		t.Fatalf("members report different fills: %d vs %d", a.LatencySamples(), b.LatencySamples())
	}

	// Channel 1 goes quiet (lost, concealed as silence on the raw path)
	// while channel 0 plays on.
	for i := 0; i < 60; i++ {
		write(seq, true)
		seq++
		m.Process()
		a.readFrame(fa)
		b.readFrame(fb)
	}
	if rms(fa) < 0.05 {
		t.Fatalf("live member went silent with its sibling: rms %.3f", rms(fa))
	}
	if rms(fb) > 1e-6 {
		t.Fatalf("lost member not silent: rms %.3f", rms(fb))
	}

	// A frame with no audio anywhere writes no samples but pins poses.
	before := g.samplesWritten
	g.WriteFrame(seq, []GroupChannel{{Pose: &Pose{X: 9}}, {}})
	if g.samplesWritten != before {
		t.Fatal("a pose-only frame advanced the sample stream")
	}

	// The last member out frees the set.
	m.RemoveSource("a")
	m.Process()
	if _, ok := m.groups["c1"]; !ok {
		t.Fatal("group freed while a member remained")
	}
	m.RemoveSource("b")
	m.Process()
	if _, ok := m.groups["c1"]; ok {
		t.Fatal("group not freed with its last member")
	}
	m.mu.Lock()
	_, reg := m.groupReg["c1"]
	m.mu.Unlock()
	if reg {
		t.Fatal("group registry entry not freed")
	}
}
