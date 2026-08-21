package engine

import (
	"sync/atomic"
	"time"
)

// ProcessStats is a snapshot of accumulated render-loop timing, the
// per-phase attribution that capacity work steers by. Durations are
// totals since the Mixer was created; divide by Ticks for per-tick
// averages.
//
// Note on the engine's no-wall-clock rule: it protects the AUDIO
// timebase (nothing audible is denominated in seconds). These counters
// are diagnostics about the host CPU, deliberately outside that rule —
// they never influence rendering.
type ProcessStats struct {
	Ticks   int64
	Changes time.Duration // changes queue drain + audibility recompute
	Prep    time.Duration // source list + per-listener peer row assembly
	In      time.Duration // In phase (jitter reads, pose evaluation)
	Across  time.Duration // Across phase (spatial encode, the N² work)
	Out     time.Duration // Out phase (reverb post, binaural encode, emit)
}

type processCounters struct {
	ticks   atomic.Int64
	changes atomic.Int64
	prep    atomic.Int64
	in      atomic.Int64
	across  atomic.Int64
	out     atomic.Int64
}

// Stats returns the accumulated totals. Safe from any goroutine.
func (m *Mixer) Stats() ProcessStats {
	return ProcessStats{
		Ticks:   m.counters.ticks.Load(),
		Changes: time.Duration(m.counters.changes.Load()),
		Prep:    time.Duration(m.counters.prep.Load()),
		In:      time.Duration(m.counters.in.Load()),
		Across:  time.Duration(m.counters.across.Load()),
		Out:     time.Duration(m.counters.out.Load()),
	}
}

// PerTick divides the totals into per-tick averages (zero Ticks yields
// zeros).
func (s ProcessStats) PerTick() ProcessStats {
	if s.Ticks == 0 {
		return ProcessStats{}
	}
	n := s.Ticks
	return ProcessStats{
		Ticks:   n,
		Changes: s.Changes / time.Duration(n),
		Prep:    s.Prep / time.Duration(n),
		In:      s.In / time.Duration(n),
		Across:  s.Across / time.Duration(n),
		Out:     s.Out / time.Duration(n),
	}
}
