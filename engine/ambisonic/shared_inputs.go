package ambisonic

// Shared input matrix (engine-added; PROVENANCE.md divergence — P3(a)
// of the capacity program). The reverb bus reads sources UNDELAYED, so
// every listener packing its own copy of every peer's frame was pure
// redundancy: N listeners × K peers × 960 B per tick. Instead, each
// source writes its frame ONCE per tick into a slot-indexed shared
// matrix, and every listener's reverb GEMM runs dense over the full
// matrix with zero weight columns masking excluded pairs (zero-weight
// dense masking, decided over BRGEMM run-selection — see
// plan/gemm-backends/brgemm-scattered.md for the priced alternative).
//
// Concurrency contract: rows are written in the In phase (each source
// touches only its own slot's row — disjoint slices), read in the
// Across phase; the phase barrier between them is the synchronization.
// Stale rows of departed or silent-slot entities are masked by zero
// weights (0 × stale = 0; stale audio is never NaN).
//
// Encoders opt in via SetSharedInputs; nil (the default) keeps the
// historical per-listener packing, which is what the copied test
// suites exercise.

import (
	"github.com/panaudia/panaudia-lasa/engine/common"
	"github.com/panaudia/panaudia-lasa/engine/gemm"
)

// SharedInputs is the per-space slot-indexed source frame matrix
// [MaxNodes × FrameSize].
type SharedInputs struct {
	data      []float32
	frameSize int
	maxNodes  int
}

func NewSharedInputs(cfg common.MixerConfig) *SharedInputs {
	return &SharedInputs{
		data:      make([]float32, cfg.MaxNodes*cfg.FrameSize),
		frameSize: cfg.FrameSize,
		maxNodes:  cfg.MaxNodes,
	}
}

// SetRow publishes one source's current frame. In-phase, owner slot
// only.
func (s *SharedInputs) SetRow(slot int, frame []float32) {
	copy(s.data[slot*s.frameSize:(slot+1)*s.frameSize], frame)
}

// SetSharedInputs opts this encoder's reverb bus into the shared
// matrix (nil reverts to per-listener packing). Set at construction
// time, before rendering.
func (encoder *Encoder) SetSharedInputs(shared *SharedInputs) {
	encoder.sharedInputs = shared
}

// scatterReverbWeights places one included pair's wet weights into the
// full-width transposed weight matrix (column = peer slot). Excluded
// slots keep the zeros from this frame's clear — the mask.
func (encoder *Encoder) scatterReverbWeights(dst []float32, slot int, wet []float32) {
	mn := encoder.mixerConfig.MaxNodes
	for c := 0; c < common.REVERB_CHANNELS; c++ {
		dst[c*mn+slot] = wet[c]
	}
}

// mixReverbShared is the dense masked reverb GEMM over the shared
// matrix: K = MaxNodes always, zero columns contributing nothing.
func (encoder *Encoder) mixReverbShared(cur, prev []float32) {
	mn := encoder.mixerConfig.MaxNodes
	gemm.EncodeFade(mn, common.REVERB_CHANNELS, mn,
		encoder.mixerConfig.FrameSize,
		encoder.sharedInputs.data, cur, prev,
		encoder.ReverbOutput, encoder.reverbSharedTemp)
}
