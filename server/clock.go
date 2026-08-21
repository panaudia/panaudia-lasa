package main

// The one clock (design §1): the render tick. The ticker targets
// accumulated time, so the tick count IS elapsed duration and the
// engine's frameStart sample index IS the time base. epoch₀ — the wall
// time at which sample 0 began — is read exactly once, when the render
// loop starts; every epoch-domain value the server ever emits derives
// from it as epoch₀ + sampleTS·10⁶/48000. There is no other wall-clock
// read on any audio path.

import (
	"sync/atomic"
	"time"

	"github.com/panaudia/panaudia-lasa/engine/engine"
	"github.com/panaudia/panaudia-lasa/engine/timing"
)

const tickMillis = 1000 * engine.FrameSize / engine.SampleRate // 5

type sampleClock struct {
	epochMicros atomic.Uint64 // unix µs of sample 0; set once in run
}

// run is the audio thread: it reads epoch₀ (the single wall-clock
// read), then drives the mixer under the accumulated-time ticker until
// the mixer closes.
func (c *sampleClock) run(m *engine.Mixer) {
	c.epochMicros.Store(uint64(time.Now().UnixMicro()))
	m.Run(timing.NewTicker(tickMillis, false))
}

// sampleEpoch is the lasa shell's Config.SampleEpoch (design §1 C5):
// sample timestamp → unix µs for the informational §5.1 anchors. Only
// ever called with timestamps of rendered frames, which cannot exist
// before run has stored epoch₀.
func (c *sampleClock) sampleEpoch(sampleTS uint64) uint64 {
	return c.epochMicros.Load() + sampleTS*1_000_000/engine.SampleRate
}
