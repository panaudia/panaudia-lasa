package engine

// The Phase A gate: the incumbent's M8 budget validation
// (direct/budget_m8_test.go), ported against the new orchestration. The
// full render loop at target scale must fit the 5 ms frame with
// headroom. Same shape as the original: 50 "people", each a tone source
// AND a binaural-decoding listener (P×(P−1) mix load), decode-then-
// discard outputs so the binaural render path is exercised without opus
// encode — matching the incumbent's ConvolverBinauralNullOutput
// listeners. The assert is a tripwire for order-of-magnitude
// regressions, not a profiler; the gate also runs on the x86 production
// box before Phase A closes.

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/panaudia/panaudia-lasa/engine/binaural"
	"github.com/panaudia/panaudia-lasa/engine/inout"
)

// addNullSink mirrors the incumbent budget test's listeners: a sink that
// decodes binaural through the shared convolver set and discards,
// skipping the opus/dynamics stage.
func addNullSink(m *Mixer, id string) error {
	k := &Sink{m: m, id: id,
		nullOut: inout.NewConvolverBinauralNullOutput(
			binaural.NewConvolverDecoder(m.convolverSet), m.channelCount)}
	return m.addSink(id, k)
}

func renderBudget(t *testing.T, people, frames int, extent bool) (median, p99 time.Duration) {
	return renderBudgetOrder(t, people, frames, 3, extent)
}

func renderBudgetOrder(t *testing.T, people, frames, order int, extent bool) (median, p99 time.Duration) {
	t.Helper()

	m, err := New(Config{
		Order:        order,
		MaxEntities:  people + 10,
		ReverbPreset: ReverbMediumRoom,
		Workers:      16,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// The incumbent placed people on a 0–1 grid in a 40 m space; same
	// geometry in metres.
	for i := 0; i < people; i++ {
		id := fmt.Sprintf("p%d", i)
		if _, err := m.AddSource(id, SourceConfig{
			TestTone:    200.0 + float64(i),
			InitialPose: Pose{X: float64(i%10) * 4.0, Y: float64(i/10) * 4.0, Z: 20.0, Yaw: float64(i)},
		}); err != nil {
			t.Fatal(err)
		}
		if err := addNullSink(m, id); err != nil {
			t.Fatal(err)
		}
		if extent {
			// Every source sized AND directional: the extent laws run on
			// every one of the P×(P−1) pairs every frame.
			p := DefaultRenderParams()
			p.Size, p.Directivity = 2.0, 0.7
			if err := m.SetRenderParams(id, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	m.Process() // applies the queued adds, warm-up render

	times := make([]time.Duration, 0, frames)
	for f := 0; f < frames; f++ {
		start := time.Now()
		m.Process()
		times = append(times, time.Since(start))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2], times[len(times)*99/100]
}

func TestRenderBudgetAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("budget run is slow")
	}
	if raceEnabled {
		t.Skip("perf gate is meaningless under the race detector")
	}
	const people = 50
	const frames = 200

	med, p99 := renderBudget(t, people, frames, false)

	t.Logf("bilateral %d people: median %v p99 %v", people, med, p99)

	if med > 4*time.Millisecond {
		t.Fatalf("median frame %v exceeds the 4 ms budget gate", med)
	}
	if p99 > 5*time.Millisecond {
		t.Fatalf("p99 frame %v exceeds the 5 ms frame budget", p99)
	}
}

// TestExtentBudgetAtScale prices the size/directivity laws at target
// scale: the same 50-person scene neutral vs every source sized (2 m)
// and directional (k = 0.7) — the extent laws running on all ~2500
// pairs every frame. The neutral run doubles as proof of the extent
// gate (neutral must match the plain budget gate). Interleaved runs,
// same process, so the comparison shares thermal/scheduler conditions.
func TestExtentBudgetAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("budget run is slow")
	}
	if raceEnabled {
		t.Skip("perf gate is meaningless under the race detector")
	}
	const people = 50
	const frames = 200

	neutral, _ := renderBudget(t, people, frames, false)
	extent, extentP99 := renderBudget(t, people, frames, true)
	delta := extent - neutral

	t.Logf("extent cost at %d×%d: neutral median %v, extent median %v (Δ %v, %+.1f%%), extent p99 %v",
		people, people, neutral, extent, delta, 100*float64(delta)/float64(neutral), extentP99)

	// Tripwires: the extent scene must still clear the frame budget,
	// and the laws must stay in the "weight-vector math" cost class —
	// the plan's bar was "no measurable Across regression"; allow noise.
	if extent > 4*time.Millisecond {
		t.Fatalf("extent median %v exceeds the 4 ms budget gate", extent)
	}
	if extentP99 > 5*time.Millisecond {
		t.Fatalf("extent p99 %v exceeds the 5 ms frame budget", extentP99)
	}
	if float64(delta) > 0.15*float64(neutral) {
		t.Errorf("extent laws cost %+.1f%% median at scale — expected weight-vector-class (<~5%%), investigate",
			100*float64(delta)/float64(neutral))
	}
}

// TestOrderBudgetSweep prices the bus order at target scale: the
// 50-person budget scene at orders 2–5 (9/16/25/36 channels — SH
// evaluation, pack, GEMM K-width and HRTF bank all scale with it).
// Informational rows for the order decision (2026-08-04); the only
// hard gate is that every supported order clears the frame budget.
func TestOrderBudgetSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("budget run is slow")
	}
	if raceEnabled {
		t.Skip("perf gate is meaningless under the race detector")
	}
	const people = 50
	const frames = 200

	for order := 2; order <= 5; order++ {
		med, p99 := renderBudgetOrder(t, people, frames, order, false)
		t.Logf("order %d (%2d ch) at %d people: median %v p99 %v", order, (order+1)*(order+1), people, med, p99)
		if p99 > 5*time.Millisecond {
			t.Errorf("order %d p99 %v exceeds the 5 ms frame budget", order, p99)
		}
	}
}
