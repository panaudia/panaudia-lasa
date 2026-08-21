package engine

// Ambi sink tests (P4): the raw ambisonic output path — classic
// world-frame render, truncation, N3D→SN3D, multistream opus.

import (
	"math"
	"sync"
	"testing"

	"github.com/panaudia/panaudia-lasa/engine/inout"
)

type collectingAmbiWriter struct {
	mu     sync.Mutex
	frames [][]byte
}

func (w *collectingAmbiWriter) WriteFrame(pkt []byte, sampleTS uint64) {
	w.mu.Lock()
	w.frames = append(w.frames, append([]byte(nil), pkt...))
	w.mu.Unlock()
}

// channelRMS decodes all frames and returns per-channel RMS over the
// tail (past codec warm-up).
func channelRMS(t *testing.T, frames [][]byte, channels int) []float64 {
	t.Helper()
	dec, err := inout.NewOpusMSDecoder(channels)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.BeforeDestroy()
	skip := len(frames) / 2
	sums := make([]float64, channels)
	var n int
	for i, f := range frames {
		pcm, err := dec.Decode(f)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if i < skip {
			continue
		}
		for s := 0; s < len(pcm)/channels; s++ {
			for c := 0; c < channels; c++ {
				v := float64(pcm[s*channels+c])
				sums[c] += v * v
			}
		}
		n += len(pcm) / channels
	}
	out := make([]float64, channels)
	for c := range sums {
		out[c] = math.Sqrt(sums[c] / float64(n))
	}
	return out
}

// TestAmbiSinkRender: a dead-ahead tone through the full ambi path.
// Asserts the wire contract: decodable multistream at the format's
// channel count, energy in W, SN3D normalisation (X/W ≈ 1 for a
// dead-ahead source — it would be √3 if the field were still N3D),
// and the world-frame rule (the listener's yaw must not rotate it).
func TestAmbiSinkRender(t *testing.T) {
	for _, tc := range []struct {
		name     string
		order    int
		channels int
	}{
		{"ambi2", 2, 9},
		{"ambi3", 3, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(testConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if _, err := m.AddSource("s", SourceConfig{TestTone: 500, InitialPose: Pose{X: 2}}); err != nil {
				t.Fatal(err)
			}
			w := &collectingAmbiWriter{}
			k, err := m.AddAmbiSink("l", tc.order, w)
			if err != nil {
				t.Fatal(err)
			}
			// World-frame rule: an aggressive listener rotation must not
			// rotate the field (position is honoured, rotation never).
			k.SetPose(Pose{Yaw: math.Pi})
			settle(m, 80)

			w.mu.Lock()
			frames := w.frames
			w.mu.Unlock()
			if len(frames) < 60 {
				t.Fatalf("only %d ambi frames", len(frames))
			}
			rms := channelRMS(t, frames, tc.channels)
			if rms[0] == 0 {
				t.Fatal("W channel silent")
			}
			// SN3D: dead-ahead source → ACN3 (X) ≈ W; ACN1 (Y) ≈ 0.
			if r := rms[3] / rms[0]; r < 0.8 || r > 1.2 {
				t.Errorf("X/W = %v, want ≈1 (SN3D; √3 would mean N3D leaked to the wire)", r)
			}
			if r := rms[1] / rms[0]; r > 0.2 {
				t.Errorf("Y/W = %v, want ≈0 — was the field head-rotated? (world-frame rule)", r)
			}
		})
	}
}

// TestAmbiAndBinauralSimultaneous: the same entity renders its binaural
// perspective and an ambi tap in the same ticks.
func TestAmbiAndBinauralSimultaneous(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.AddSource("s", SourceConfig{TestTone: 500, InitialPose: Pose{Y: 2}}); err != nil {
		t.Fatal(err)
	}
	bw := &collectingWriter{}
	if _, err := m.AddSink("l", bw); err != nil {
		t.Fatal(err)
	}
	aw := &collectingAmbiWriter{}
	if _, err := m.AddAmbiSink("l", 2, aw); err != nil {
		t.Fatal(err)
	}
	settle(m, 40)

	if len(bw.frames) < 30 {
		t.Fatalf("binaural starved: %d frames", len(bw.frames))
	}
	aw.mu.Lock()
	af := len(aw.frames)
	aw.mu.Unlock()
	if af < 30 {
		t.Fatalf("ambi starved: %d frames", af)
	}
	rmsL, rmsR := stereoFrames(t, bw.frames)
	if rmsL[len(rmsL)-1] == 0 && rmsR[len(rmsR)-1] == 0 {
		t.Fatal("binaural silent beside the ambi tap")
	}
}

// TestAmbiSinkLifecycle: registry accounting, duplicate refusal, order
// validation, and clean teardown through RemoveAmbiSink.
func TestAmbiSinkLifecycle(t *testing.T) {
	cfg := testConfig()
	cfg.Order = 2
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if _, err := m.AddAmbiSink("a", 3, &collectingAmbiWriter{}); err == nil {
		t.Fatal("ambi3 on an order-2 bus must be refused")
	}
	if _, err := m.AddAmbiSink("a", 1, &collectingAmbiWriter{}); err == nil {
		t.Fatal("order-1 ambi must be refused")
	}

	if _, err := m.AddAmbiSink("a", 2, &collectingAmbiWriter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddAmbiSink("a", 2, &collectingAmbiWriter{}); err == nil {
		t.Fatal("duplicate (entity, order) ambi sink must be refused")
	}
	settle(m, 2)
	if got := m.Entities(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("registry = %v, want [a]", got)
	}

	m.RemoveAmbiSink("a", 2)
	settle(m, 2)
	if got := m.Entities(); len(got) != 0 {
		t.Fatalf("registry after removal = %v, want empty", got)
	}
}

// TestAmbiProcessAllocs: the ambi render/encode path stays off the heap.
func TestAmbiProcessAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation accounting differs under the race detector")
	}
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.AddSource("s", SourceConfig{TestTone: 500, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddAmbiSink("l", 2, &nullAmbiWriter{}); err != nil {
		t.Fatal(err)
	}
	settle(m, 10)
	allocs := testing.AllocsPerRun(200, func() { m.Process() })
	if allocs != 0 {
		t.Fatalf("Process with an ambi sink allocates %v per tick, want 0", allocs)
	}
}

type nullAmbiWriter struct{}

func (nullAmbiWriter) WriteFrame([]byte, uint64) {}
