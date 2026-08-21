package buffers

// This file carries, verbatim, the small vocabulary JitterBuffer shares with
// the legacy circular-buffer implementations that were deliberately left
// behind in the panaudia repo (circular_buffer.go / circular_buffer_a.go —
// directroc-only; see PROVENANCE.md). JitterBuffer implements ICircularBuffer
// and reports CircularBufferStats; nothing else of the legacy files survives.

const (
	stateFilling = "filling"
	statePlaying = "playing"
)

// ICircularBuffer is the common interface for circular buffer implementations.
type ICircularBuffer interface {
	Write(src []float32)
	Read(dst []float32) bool
	GetBehind() int
	GetStats() CircularBufferStats
}

func defaultVal(val, def int) int {
	if val == 0 {
		return def
	}
	return val
}

// CircularBufferStats holds diagnostic information about the buffer state.
type CircularBufferStats struct {
	FillLevelSamples int
	FillLevelMs      float64
	CurrentZone      int // -1, 0, or +1 (0 = target window)
	UnderrunCount    int
	OverrunCount     int
	SamplesDropped   int
	SamplesInserted  int
	State            string // "filling" or "playing"
}
