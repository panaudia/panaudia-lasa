package jitterlab

// scenario.go — virtual-time event schedules (plan/jitter-v4-harness.md).
// Time is a master clock counted in frames at the geometry's sample rate;
// no wall-clock anywhere. A scenario is a merged, time-ordered event
// list; at equal times writes are ordered before reads (deterministic tie
// rule — an arrival at instant t is visible to the read at t). Schedules
// are built by the generator pipeline in model.go; Calm and IIDJitter
// below are thin wrappers over it.

// Kind is the event type.
type Kind uint8

const (
	// KindWrite delivers Frames frames (stream positions Pos..Pos+Frames)
	// to the buffer.
	KindWrite Kind = iota
	// KindRead consumes Frames frames from the buffer.
	KindRead
	// KindLost marks Frames frames of source stream (at Pos) that were
	// sent but never delivered — the stream advances, nothing is written.
	// Scheduled at the packet's send time; only position accounting.
	KindLost
)

// Event is one scheduled call against the buffer under test.
type Event struct {
	Time   int64 // master clock, frames
	Kind   Kind
	Frames int64
	Pos    int64 // stream position of first frame (writes/losses only)
	// Conceal marks a KindWrite whose content is upstream concealment
	// (zeros) rather than source audio — the FEC reassembler's
	// gave-up-and-concealed frame for an unrepairable position. The
	// stream stays continuous; the runner attributes the resulting
	// silence and position jump to upstream loss, not the buffer.
	Conceal bool
}

// Geometry is the frame configuration for a scenario.
type Geometry struct {
	SampleRate   int
	NumChannels  int
	WriterFrames int64 // W: frames per write (wire frame)
	ReaderFrames int64 // R: frames per read (callback frame)
}

// DefaultGeometry is current production: 48 kHz mono, W = R = 5 ms.
func DefaultGeometry() Geometry {
	return Geometry{SampleRate: 48000, NumChannels: 1, WriterFrames: 240, ReaderFrames: 240}
}

// LegacyGeometry is v3's design assumption: 20 ms wire frames against
// 5 ms reads. Kept as the second point on the harness's geometry axis.
func LegacyGeometry() Geometry {
	return Geometry{SampleRate: 48000, NumChannels: 1, WriterFrames: 960, ReaderFrames: 240}
}

// BrowserGeometry is the browser playout buffer's frame configuration:
// the server's 5 ms Opus frames read in 128-sample AudioWorklet quanta
// (ratio 1.875, non-integer). Third point on the geometry axis, added
// for the TS port (plan/jitter-v4-ts-port.md phase 0).
func BrowserGeometry() Geometry {
	return Geometry{SampleRate: 48000, NumChannels: 1, WriterFrames: 240, ReaderFrames: 128}
}

// MsToFrames converts milliseconds to frames at the geometry's rate.
func (g Geometry) MsToFrames(ms float64) int64 {
	return int64(ms * float64(g.SampleRate) / 1000.0)
}

// Scenario is a named, fully materialized schedule.
type Scenario struct {
	Name     string
	Geometry Geometry
	Duration int64 // master clock frames
	Events   []Event

	// Ground truth carried for the oracle and the sensor lab.
	WriterPPM float64
}

// mergeEvents merges two time-sorted slices; ties order writes first.
func mergeEvents(writes, reads []Event) []Event {
	out := make([]Event, 0, len(writes)+len(reads))
	i, j := 0, 0
	for i < len(writes) && j < len(reads) {
		if writes[i].Time <= reads[j].Time {
			out = append(out, writes[i])
			i++
		} else {
			out = append(out, reads[j])
			j++
		}
	}
	out = append(out, writes[i:]...)
	out = append(out, reads[j:]...)
	return out
}

// Calm is the control scenario: constant transit delay, no jitter.
// Expect zero artifacts after warm-up and latency ≈ oracle.
func Calm(g Geometry, dur, delayFrames int64) Scenario {
	return Build("calm", g, dur, WriterModel{BaseDelay: delayFrames}, ReaderModel{})
}

// IIDJitter is the classic-variance scenario: per-packet delay =
// base + N(0, sigma), truncated at 0, FIFO-clamped. Deterministic for a
// given seed.
func IIDJitter(g Geometry, dur, baseFrames, sigmaFrames int64, seed uint64) Scenario {
	return Build("iid-jitter", g, dur,
		WriterModel{BaseDelay: baseFrames, JitterSigma: sigmaFrames, Seed: seed}, ReaderModel{})
}
