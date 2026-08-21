package buffers

// StreamReadPos returns the reader's absolute position in the written
// sample stream (per-channel samples — for a mono buffer, samples): the
// index just past the last sample consumed by Read. 0 until the first
// successful Read. Snap corrections (startup, lap, overrun, underrun)
// move it discontinuously, which is exactly what a consumer aligning
// per-sample metadata to the played stream wants.
//
// Engine-added file, not part of the panaudia copy (see PROVENANCE.md):
// the pose store records pose timestamps at Write time in this same
// stream domain and uses this position to evaluate the pose of the
// frame being rendered.
func (j *JitterBuffer) StreamReadPos() int64 {
	return j.readPos.Load()
}
