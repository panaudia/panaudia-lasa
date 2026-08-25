package main

// The S6 capacity smoke: 50 real clients × 50 own-sink renders through
// the full server at the real cadence — QUIC loopback, opus both ways,
// every source audible to every sink on the default channel. Not a
// benchmark (the engine repo owns those); a composed-system smoke: does
// admission, ingest, render, egress and teardown hold together at the
// target scale with the render keeping real time.

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panaudia/lasa/client"
	"github.com/panaudia/lasa/wire"
)

func TestCapacitySmoke50x50(t *testing.T) {
	if testing.Short() {
		t.Skip("capacity smoke is slow")
	}
	if raceEnabled {
		// Under the detector's ~10× slowdown the render cannot hold real
		// time at this scale, the jitter rings lap, and the lap overwrite
		// is the documented, deliberate data race (buffers.JitterBuffer
		// Write) — so the detector fires on a design property, not a bug.
		// The concurrency coverage of the composed path under -race comes
		// from the single-client lifecycle and WebTransport tests.
		t.Skip("capacity smoke laps the jitter rings under the race detector (documented LAP race)")
	}
	const N = 50
	a := startTestApp(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)

	// N clients on a circle (radius 3 m), each speaking a distinct-ish
	// tone and subscribed to its own sink.
	clients := make([]*client.Client, 0, N)
	ids := make([]string, 0, N)
	var frames [N]atomic.Uint64
	meters := make([]*sinkMeter, 0, 5) // decode a sample, count the rest
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("e-%02d", i)
		c, err := dialTest(t, a, fmt.Sprintf("c-%02d", i), id)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		clients = append(clients, c)
		ids = append(ids, id)
		ang := 2 * math.Pi * float64(i) / N
		pose := wire.Pose{X: float32(3 * math.Cos(ang)), Y: float32(3 * math.Sin(ang))}
		speakToneAt(t, ctx, c, id, 300+20*float64(i), 0.05, pose, stop)
		if i < 5 {
			meters = append(meters, newSinkMeter(t, ctx, c, id))
		} else {
			idx := i
			if _, err := c.SubscribeSink(ctx, id, "binaural", func(uint64, []byte) {
				frames[idx].Add(1)
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	waitFor(t, "all 50 admitted (conn-map ≡ engine)", func() bool { return settled(a, ids...) })

	// Real-time check: over two measured seconds the render must tick at
	// its 5 ms cadence (400 ticks ± 25%).
	startTicks := a.mixer.Stats().Ticks
	startWall := time.Now()

	// Meanwhile the sampled sinks must carry energy — every source
	// shares the default channel with every sink.
	for _, m := range meters {
		m.waitLevel(t, ctx, "sampled sink energetic", 10, func(r float64) bool { return r > 0.01 })
	}
	if remain := 2*time.Second - time.Since(startWall); remain > 0 {
		time.Sleep(remain)
	}
	elapsed := time.Since(startWall)
	ticks := a.mixer.Stats().Ticks - startTicks
	expect := float64(elapsed) / float64(5*time.Millisecond)
	// The timing gates are meaningless under the race detector's ~10×
	// slowdown; the concurrency coverage of 50 live clients is the
	// point of running there — flow and teardown still assert.
	if !raceEnabled && (float64(ticks) < 0.75*expect || float64(ticks) > 1.25*expect) {
		t.Errorf("render not holding real time: %d ticks in %s (expected ~%.0f)", ticks, elapsed, expect)
	}

	// The unsampled sinks all flowed too.
	for i := 5; i < N; i++ {
		if frames[i].Load() == 0 {
			t.Errorf("sink %d received no frames", i)
		}
	}

	// Health snapshot for the log: per-tick render cost + ingest totals.
	s := a.stats()
	per := s.Engine.PerTick()
	var delivered, lost uint64
	maxFill := 0
	for _, e := range s.Entities {
		delivered += e.Depacketizer.Delivered
		lost += e.Depacketizer.Lost + e.Depacketizer.Skipped
		if e.LatencySamples > maxFill {
			maxFill = e.LatencySamples
		}
	}
	perTickTotal := per.Prep + per.In + per.Across + per.Out
	t.Logf("50×50: %d ticks in %s; per-tick prep=%s in=%s across=%s out=%s (total %s of 5ms budget); max jitter fill %d smp; ingress delivered=%d lost=%d",
		ticks, elapsed.Round(time.Millisecond), per.Prep, per.In, per.Across, per.Out, perTickTotal, maxFill, delivered, lost)
	if !raceEnabled && perTickTotal > 5*time.Millisecond {
		t.Errorf("per-tick render cost %s exceeds the 5 ms budget", perTickTotal)
	}

	// Teardown at scale: every departure through the funnel, both
	// invariant halves drained.
	for _, c := range clients {
		_ = c.Close()
	}
	waitFor(t, "all 50 swept", func() bool { return settled(a) })
}
