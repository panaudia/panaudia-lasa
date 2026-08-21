package engine

// The capacity sweep — P0′ of the optimization program (plan: the
// per-node capacity curve is a product metric; the cloud shards
// rendering across nodes, so single-node headroom directly buys shard
// count). Unlike the budget test (a pass/fail tripwire at the design
// floor of 50), the sweep is observational: it logs the frame-time
// curve and per-phase attribution across N so the N² terms can be
// ranked and every engine change reprices the shard math.
//
// Workload: the all-active dense case — N entities, every one a tone
// source AND a binaural-decoding listener (N×(N−1) pairs), with
// listeners in continuous motion (fresh sink poses every tick) so the
// weight, delay-ramp and head-frame paths do real per-pair work rather
// than settling into static fast paths.

import (
	"fmt"
	"math"
	"sort"
	"testing"
	"time"
)

func runSweep(t *testing.T, people, frames int) (median, p99 time.Duration, stats ProcessStats) {
	t.Helper()

	m, err := New(Config{
		Order:        3,
		MaxEntities:  people + 10,
		ReverbPreset: ReverbMediumRoom,
		Workers:      16,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sinks := make([]*Sink, people)
	for i := 0; i < people; i++ {
		id := fmt.Sprintf("p%d", i)
		if _, err := m.AddSource(id, SourceConfig{
			TestTone:    200.0 + float64(i),
			InitialPose: Pose{X: float64(i%10) * 4.0, Y: float64(i/10) * 4.0, Z: 20.0},
		}); err != nil {
			t.Fatal(err)
		}
		if err := addNullSink(m, id); err != nil {
			t.Fatal(err)
		}
		sinks[i] = m.reg[id].sink
	}

	move := func(frame int) {
		// Deterministic slow orbit + head turn per listener: enough
		// motion that every pair's weights, delays and head frame
		// change every tick, as they do in a live space.
		for i, k := range sinks {
			ph := float64(frame)*0.01 + float64(i)*0.7
			k.SetPose(Pose{
				X:   float64(i%10)*4.0 + math.Cos(ph),
				Y:   float64(i/10)*4.0 + math.Sin(ph),
				Z:   20.0,
				Yaw: math.Sin(ph * 0.5),
			})
		}
	}

	for f := 0; f < 5; f++ { // warm-up
		move(f)
		m.Process()
	}
	base := m.Stats()

	times := make([]time.Duration, 0, frames)
	for f := 0; f < frames; f++ {
		move(f + 5)
		start := time.Now()
		m.Process()
		times = append(times, time.Since(start))
	}
	after := m.Stats()

	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	stats = ProcessStats{
		Ticks:   after.Ticks - base.Ticks,
		Changes: after.Changes - base.Changes,
		Prep:    after.Prep - base.Prep,
		In:      after.In - base.In,
		Across:  after.Across - base.Across,
		Out:     after.Out - base.Out,
	}
	return times[len(times)/2], times[len(times)*99/100], stats
}

func TestCapacitySweep(t *testing.T) {
	if testing.Short() {
		t.Skip("sweep is slow")
	}
	if raceEnabled {
		t.Skip("capacity numbers are meaningless under the race detector")
	}

	const frames = 150
	for _, n := range []int{50, 100, 200} {
		med, p99, s := runSweep(t, n, frames)
		pt := s.PerTick()
		t.Logf("N=%d: median %v p99 %v (%.0f%% of 5ms) | per tick: changes %v prep %v in %v across %v out %v",
			n, med, p99, float64(med)/float64(5*time.Millisecond)*100,
			pt.Changes, pt.Prep, pt.In, pt.Across, pt.Out)
	}
}
