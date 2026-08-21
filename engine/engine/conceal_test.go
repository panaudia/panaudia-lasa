package engine

import (
	"math"
	"testing"

	opus "gopkg.in/hraban/opus.v2"
)

// Conceal must advance the stream exactly as the lost packet would
// have: sample accounting (and therefore pose alignment) stays intact,
// and the decoder keeps working across the gap.
func TestConcealKeepsStreamAccounting(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	src, err := m.AddSource("s", SourceConfig{InitialPose: Pose{X: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("l", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}

	// Real opus packets (Conceal drives real decoder state).
	enc, err := opus.NewEncoder(SampleRate, 1, opus.AppVoIP)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, FrameSize)
	pkt := make([]byte, 4000)
	encodeFrame := func(f int) []byte {
		for i := range pcm {
			pcm[i] = float32(0.4 * math.Sin(float64(f*FrameSize+i)*0.05))
		}
		n, err := enc.EncodeFloat32(pcm, pkt)
		if err != nil {
			t.Fatal(err)
		}
		return pkt[:n]
	}

	const total, lost = 40, 20
	var postGapEnergy float64
	for f := 0; f < total; f++ {
		p := Pose{X: 1 + float64(f)*0.1}
		if f == lost {
			src.Conceal(uint64(f+1), FrameSize)
			continue
		}
		src.WriteOpus(uint64(f+1), &p, encodeFrame(f))
		m.Process()
		if f > lost+4 { // decoder well past the gap, buffer still playing
			for _, v := range m.ents["l"].enc.Output {
				postGapEnergy += float64(v) * float64(v)
			}
		}
	}

	// Accounting: every packet, concealed included, advanced the stream.
	if got, want := src.samplesWritten, uint64(total*FrameSize); got != want {
		t.Fatalf("stream accounting: %d samples written, want %d", got, want)
	}
	// The pose ring's newest entry sits at the true sender position.
	var scratch [poseRingSize]poseSample
	n := src.ring.snapshot(&scratch)
	if n == 0 || scratch[0].endSample != uint64(total*FrameSize) {
		t.Fatalf("pose ring end %d, want %d", scratch[0].endSample, total*FrameSize)
	}
	// Decoder survived the gap: audio after the concealment rendered
	// with real energy while the stream was live.
	if postGapEnergy == 0 {
		t.Fatal("no audio rendered after concealment gap")
	}
}

// Loudness is measured from the rendered frame and published post-gain.
func TestLoudnessPostGain(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	src, err := m.AddSource("s", SourceConfig{TestTone: 500, InitialPose: Pose{X: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("l", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		m.Process()
	}
	loud := src.Loudness()
	if loud <= 0 {
		t.Fatal("tone source reports zero loudness")
	}

	// Gain applies at publish: zero gain zeroes loudness immediately.
	p := DefaultRenderParams()
	p.Gain = 0
	if err := m.SetRenderParams("s", p); err != nil {
		t.Fatal(err)
	}
	m.Process()
	m.Process()
	if got := src.Loudness(); got != 0 {
		t.Fatalf("post-gain loudness with gain 0: %g, want 0", got)
	}

	// And scales: gain 2 ≈ 2× the gain-1 reading (EMA state unchanged).
	p.Gain = 2
	if err := m.SetRenderParams("s", p); err != nil {
		t.Fatal(err)
	}
	m.Process()
	m.Process()
	if got := src.Loudness(); math.Abs(float64(got)/float64(loud)-2) > 0.2 {
		t.Fatalf("gain-2 loudness %g vs gain-1 %g: ratio %f, want ≈2", got, loud, got/loud)
	}
}
