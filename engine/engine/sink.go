package engine

import (
	"github.com/panaudia/panaudia-lasa/engine/inout"
)

// FrameWriter receives a sink's rendered frames on a render worker
// goroutine. WriteFrame must not block: the opus slice aliases a reused
// buffer and is valid only until WriteFrame returns (send or copy
// synchronously — the raw zero-copy contract of the server SPI).
// sampleTS is the frame's first sample on the mixer's GLOBAL sample
// clock ((tick−1)×240 — one clock for every sink, so any two frames'
// timestamps are directly comparable; design §1). The engine has no
// wall-clock; epoch mapping is the host's concern.
type FrameWriter interface {
	WriteFrame(opus []byte, sampleTS uint64)
}

// Sink is an entity's audio-out half: the per-listener binaural render
// target. SetPose may be called from any single goroutine (the network
// receive path); the render loop reads the latest pose once per tick.
type Sink struct {
	m  *Mixer
	id string

	w   FrameWriter
	out *inout.OpusOutputEncoder

	// nullOut, when set, replaces the opus path with a decode-then-
	// discard output (test/diagnostic parity with the incumbent's
	// budget-test listeners). Internal: built by addNullSink.
	nullOut inout.AmbisonicOutput

	pose poseSlot
}

// SetPose updates the sink's pose, latest-wins. For a sink the rotation
// path wants freshness, not audio alignment: the value is read once per
// tick and consumed by both the bilateral encode and the binaural render,
// so the two can never use different rotations within a frame.
func (k *Sink) SetPose(p Pose) {
	k.pose.store(p)
}

// emit runs on a render worker in the Out phase.
func (k *Sink) emit(output []float32) {
	if k.nullOut != nil {
		k.nullOut.WriteAmbisonic(output)
		return
	}
	pkt := k.out.Encode(output)
	k.w.WriteFrame(pkt, k.m.frameStart)
}

// destroy releases cgo resources. Audio thread only, after the entity has
// stopped rendering — the funnel lesson carried as a rule.
func (k *Sink) destroy() {
	if k.out != nil {
		k.out.BeforeDestroy()
	}
	if k.nullOut != nil {
		k.nullOut.BeforeDestroy()
	}
}
