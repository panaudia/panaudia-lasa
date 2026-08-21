package jitterlab

// v4.go — thin adapter binding the jitterlab harness to the PRODUCTION
// v4 core (buffers.JitterBuffer) after the §13 phase-2 swap. The full
// acceptance suite (A1–A9, knob frontier, FEC floor, variant matrix,
// multi-seed spreads) runs against the shipped code; only the
// frames↔durations translation lives here. The controller variant seam
// and its alternatives are in v4_variants.go.

import (
	"time"

	"github.com/panaudia/panaudia-lasa/engine/buffers"
)

// Aliases: the harness speaks the production types.
type (
	V4Buffer       = buffers.JitterBuffer
	Controller     = buffers.Controller
	WindowEstimate = buffers.WindowEstimate
	ServoGeometry  = buffers.ServoGeometry
	PServo         = buffers.PServo
)

// V4Config mirrors buffers.JitterBufferConfig in frame units (the
// harness's native currency).
type V4Config struct {
	SampleRate   int
	NumChannels  int
	WriterFrames int64
	ReaderFrames int64
	SafetyFrames int64

	FloorFrames    int64
	Robustness     float64
	CapacityFrames int64
	Controller     Controller
}

// NewV4Buffer constructs the production buffer from a frames-based
// config. Frame geometry passes as exact frame counts (durations
// cannot express e.g. the 128-sample worklet quantum); the remaining
// latency fields are ms-derived in every scenario, so their duration
// round-trip is exact.
func NewV4Buffer(cfg V4Config) *V4Buffer {
	sr := cfg.SampleRate
	if sr == 0 {
		sr = 48000
	}
	dur := func(frames int64) time.Duration {
		return time.Duration(frames) * time.Second / time.Duration(sr)
	}
	return buffers.NewJitterBuffer(buffers.JitterBufferConfig{
		SampleRate:   sr,
		NumChannels:  cfg.NumChannels,
		WriterFrames: cfg.WriterFrames,
		ReaderFrames: cfg.ReaderFrames,
		Safety:       dur(cfg.SafetyFrames),
		Floor:        dur(cfg.FloorFrames),
		Robustness:   cfg.Robustness,
		Capacity:     dur(cfg.CapacityFrames),
		Controller:   cfg.Controller,
	})
}

// V4QualityLevel is the frames-based view of buffers.QualityLevel.
func V4QualityLevel(g Geometry, level int) (floorFrames int64, robustness float64, capacityFrames int64) {
	floor, rob, capacity := buffers.QualityLevel(level)
	toFrames := func(d time.Duration) int64 {
		return int64(d) * int64(g.SampleRate) / int64(time.Second)
	}
	return toFrames(floor), rob, toFrames(capacity)
}
