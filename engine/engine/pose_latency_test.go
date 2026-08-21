package engine

// Pose latency harness — the companion to the chirp harness. The
// engine's two pose pipelines have deliberately different semantics,
// so two measurements with different assertions:
//
//   - SINK pose (latest-wins slot): a head turn must reach the render
//     fast. Measured: listener yaw steps 180° at a known tick; the
//     binaural image of a hard-left source flips to hard-right; the
//     flip's position in the decoded output, minus the step tick's
//     frame position, is the sink-pose latency. Expect ~a frame or
//     two (slot read next tick + in-frame weight fade + codec).
//
//   - SOURCE pose (jitter-aligned ring): a source's movement must stay
//     COHERENT with its audio, i.e. arrive with the same delay as the
//     samples it was sent with — freshness is deliberately traded for
//     sync. Measured: tone onset and a later left→right pose step are
//     placed at known positions in the SAME input stream; in the
//     output, the distance between heard onset and heard flip must
//     equal their distance in the input stream (the stream-speed
//     identity), independent of what the jitter latency happens to be.
//
// Both detect via per-frame L/R RMS dominance with hysteresis — at
// 2 kHz the HRTF's ILD for a ±90° source is large and unambiguous.

import (
	"math"
	"testing"

	opus "gopkg.in/hraban/opus.v2"
)

// stereoFrames decodes the sink's opus stream keeping ears separate:
// per-frame L/R RMS.
func stereoFrames(t *testing.T, frames [][]byte) (rmsL, rmsR []float64) {
	t.Helper()
	dec, err := opus.NewDecoder(SampleRate, 2)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]float32, 2*FrameSize)
	for _, f := range frames {
		n, err := dec.DecodeFloat32(f, buf)
		if err != nil {
			t.Fatal(err)
		}
		var l, r float64
		for i := 0; i < n; i++ {
			l += float64(buf[2*i]) * float64(buf[2*i])
			r += float64(buf[2*i+1]) * float64(buf[2*i+1])
		}
		rmsL = append(rmsL, math.Sqrt(l/float64(n)))
		rmsR = append(rmsR, math.Sqrt(r/float64(n)))
	}
	return
}

// firstFrom returns the first frame index >= start where pred holds and
// keeps holding for `hold` consecutive frames; -1 if never.
func firstFrom(start, hold int, n int, pred func(int) bool) int {
	for i := start; i < n; i++ {
		ok := true
		for j := i; j < i+hold && j < n; j++ {
			if !pred(j) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func TestSinkPoseLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency harness is slow")
	}
	m, err := New(Config{Order: 3, MaxEntities: 4, ReverbPreset: ReverbNone, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// Tone source hard left of an origin listener (y = left).
	if _, err := m.AddSource("tone", SourceConfig{TestTone: 2000, InitialPose: Pose{Y: 2}}); err != nil {
		t.Fatal(err)
	}
	w := &collectingWriter{}
	sink, err := m.AddSink("head", w)
	if err != nil {
		t.Fatal(err)
	}

	const totalTicks, stepTick = 400, 200
	for tick := 1; tick <= totalTicks; tick++ {
		if tick == stepTick {
			sink.SetPose(Pose{Yaw: math.Pi}) // about-face: left source → right ear
		}
		m.Process()
	}

	rmsL, rmsR := stereoFrames(t, w.frames)
	n := len(rmsL)

	// Sanity: left-dominant before the step.
	pre := firstFrom(50, 10, n, func(i int) bool { return rmsL[i] > rmsR[i]*1.2 })
	if pre < 0 || pre > stepTick-20 {
		t.Fatalf("no stable left dominance before the step (first at %d)", pre)
	}
	// The flip. Frame i of the output is tick i+1's frame.
	flip := firstFrom(stepTick-2, 10, n, func(i int) bool { return rmsR[i] > rmsL[i]*1.2 })
	if flip < 0 {
		t.Fatal("image never flipped after the pose step")
	}
	latencySamples := (flip - (stepTick - 1)) * FrameSize
	t.Logf("sink pose latency: %d frames = %d samples (%.1f ms)",
		flip-(stepTick-1), latencySamples, float64(latencySamples)*1000/SampleRate)

	// Tripwire: the rotation path must stay fast — within ~4 frames
	// (slot read + fade + codec smear), never anywhere near jitter depth.
	if latencySamples < 0 || latencySamples > 4*FrameSize {
		t.Errorf("sink pose latency %d samples outside 0..%d", latencySamples, 4*FrameSize)
	}
}

func TestSourcePoseAudioCoherence(t *testing.T) {
	if testing.Short() {
		t.Skip("latency harness is slow")
	}
	m, err := New(Config{Order: 3, MaxEntities: 4, ReverbPreset: ReverbNone, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	src, err := m.AddSource("mover", SourceConfig{InitialPose: Pose{Y: 2}})
	if err != nil {
		t.Fatal(err)
	}
	w := &collectingWriter{}
	if _, err := m.AddSink("listener", w); err != nil {
		t.Fatal(err)
	}

	// One input stream, two marked events: tone onset at packet
	// toneAt, pose step (left→right) at packet stepAt. The pose rides
	// the packets, as on the wire (5 ms frames, the default writer
	// geometry — LASA §5.1).
	const packetSamples = FrameSize
	const packets, toneAt, stepAt = 480, 40, 240
	packet := make([]float32, packetSamples)
	silence := make([]float32, packetSamples)
	phase := 0.0
	for p := 0; p < packets; p++ {
		pcm := silence
		if p >= toneAt {
			for i := range packet {
				packet[i] = float32(0.5 * math.Sin(phase))
				phase += 2 * math.Pi * 2000 / SampleRate
			}
			pcm = packet
		}
		pose := Pose{Y: 2} // hard left
		if p >= stepAt {
			pose = Pose{Y: -2} // hard right
		}
		src.WritePCM(uint64(p+1), &pose, pcm)
		m.Process()
	}

	rmsL, rmsR := stereoFrames(t, w.frames)
	n := len(rmsL)

	// Heard onset: first frame with real energy.
	var peak float64
	for i := range rmsL {
		if v := rmsL[i] + rmsR[i]; v > peak {
			peak = v
		}
	}
	onset := firstFrom(0, 5, n, func(i int) bool { return rmsL[i]+rmsR[i] > peak*0.1 })
	if onset < 0 {
		t.Fatal("tone never heard")
	}
	// Heard flip.
	flip := firstFrom(onset, 10, n, func(i int) bool { return rmsR[i] > rmsL[i]*1.2 })
	if flip < 0 {
		t.Fatal("pose step never heard")
	}

	// The stream-speed identity: distance between the two heard events
	// must equal their distance in the input stream. Input positions:
	// onset at toneAt·packetSamples; the pose lerps between the
	// endSample of packet stepAt-1 and of packet stepAt, crossing the
	// midline around (stepAt+0.5)·packetSamples.
	heard := (flip - onset) * FrameSize
	sent := int((float64(stepAt)+0.5)*packetSamples) - toneAt*packetSamples
	skew := heard - sent
	t.Logf("source pose/audio coherence: sent gap %d samples, heard gap %d samples, skew %d samples (%.1f ms)",
		sent, heard, skew, float64(skew)*1000/SampleRate)

	// Tripwire: coherence within ~15 ms (pose lerp span one packet +
	// frame and detector granularity). A jitter-depth skew (~500+
	// samples of pure lag would already be visible; freshness-leak
	// would show as a large NEGATIVE skew) means an alignment
	// regression.
	if skew < -720 || skew > 720 {
		t.Errorf("pose/audio skew %d samples outside ±720 — jitter alignment broken?", skew)
	}
}
