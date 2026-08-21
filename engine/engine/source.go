package engine

import (
	"math"
	"sync/atomic"

	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/inout"
)

// Source is an entity's audio-in half. Write methods run on the caller's
// goroutine (the network receive path) and never block on the render
// loop: decode and jitter-write happen here, at packet cadence, exactly
// as in the incumbent; the mixer tick only ever reads.
//
// The caller delivers packets in arrival order and stops calling Write
// after RemoveSource; a straggler write is harmless (it touches only the
// source's own buffers, never the mixer's structures).
type Source struct {
	m  *Mixer
	id string

	jitter  *buffers.JitterBuffer
	decoder *inout.OpusInputDecoder
	tone    *inout.SineMonoInput

	ring        poseRing
	poseScratch [poseRingSize]poseSample // audio-thread scratch

	samplesWritten uint64 // writer-goroutine only: stream domain of the ring
	loudness       atomic.Uint32
	loudEMA        float32 // audio-thread-only EMA state
	initialPose    Pose
}

// WriteOpus ingests one packet: pose (nil = pose decimated) and Opus
// payload (empty = pose-only packet), with the gapless packet seq. Decode
// and jitter-write run here on the caller's goroutine.
func (s *Source) WriteOpus(seq uint64, pose *Pose, pkt []byte) {
	if s.tone != nil {
		return
	}
	if len(pkt) > 0 {
		pcm := s.decoder.Decode(pkt)
		s.ingestPCM(pcm)
	}
	if pose != nil {
		s.ring.push(seq, s.samplesWritten, *pose)
	}
}

// WritePCM is the decoded-elsewhere leg: mono float32 at 48 kHz. Same
// pose and seq semantics as WriteOpus.
func (s *Source) WritePCM(seq uint64, pose *Pose, pcm []float32) {
	if s.tone != nil {
		return
	}
	if len(pcm) > 0 {
		s.ingestPCM(pcm)
	}
	if pose != nil {
		s.ring.push(seq, s.samplesWritten, *pose)
	}
}

func (s *Source) ingestPCM(pcm []float32) {
	s.jitter.Write(pcm)
	s.samplesWritten += uint64(len(pcm))
}

// Conceal covers one lost packet of `samples` duration with the
// decoder's packet-loss concealment (the Depacketizer's Lost signal —
// design §3). Like Write*, it runs on the caller's goroutine and MUST
// be called in stream order, in the lost packet's place: it advances
// both the decoder state and the sample accounting, so the pose ring
// stays exactly aligned with the sender's timeline under loss. seq is
// the lost packet's sequence, for the ordering contract and
// diagnostics; no pose is pushed (the lost packet's pose is lost —
// the ring's bracketing lerp rides over it).
func (s *Source) Conceal(seq uint64, samples int) {
	_ = seq
	if s.tone != nil || s.decoder == nil {
		return
	}
	pcm := s.decoder.ConcealFloat32(samples)
	s.jitter.Write(pcm)
	s.samplesWritten += uint64(len(pcm))
}

// Loudness is the entity's presence loudness: the EMA-smoothed RMS of
// the audio actually being RENDERED (post-jitter — a starving source
// decays to silence), multiplied by the entity's current render gain
// (base profile: post-render.gain, pre-attenuation). Computed every
// frame on the audio thread (design §2); safe to read from any
// goroutine.
func (s *Source) Loudness() float32 {
	return math.Float32frombits(s.loudness.Load())
}

// loudnessAlpha is the per-frame EMA weight: ≈100 ms time constant at
// the 5 ms frame cadence (α = 1 − e^(−5/100)).
const loudnessAlpha = 0.049

// updateRenderLoudness runs on the audio thread, once per frame, with
// the frame just read from the jitter buffer and the entity's current
// gain (audio-thread state — no cross-thread mirroring needed).
func (s *Source) updateRenderLoudness(frame []float32, gain float64) {
	var sum float64
	for _, v := range frame {
		sum += float64(v) * float64(v)
	}
	rms := float32(math.Sqrt(sum / float64(len(frame))))
	s.loudEMA += loudnessAlpha * (rms - s.loudEMA)
	s.loudness.Store(math.Float32bits(s.loudEMA * float32(gain)))
}

// LatencySamples reports the samples currently buffered between ingest
// and the render read — the transport latency the adaptive jitter
// buffer is imposing on this source right now. It is the dominant and
// only *variable* term of engine latency; tick quantization (0–240
// samples), fixed DSP group delay and codec lookahead come on top (see
// latency_test.go for the measured total). Safe from any goroutine.
// Tone sources report 0.
func (s *Source) LatencySamples() int {
	if s.jitter == nil {
		return 0
	}
	return s.jitter.GetBehind()
}

// JitterStats returns the ingest jitter buffer's rich v3 snapshot —
// fill, live window allowances, underruns/overruns/laps and the ±1
// correction counters. The per-source debugging view behind the
// LatencySamples headline number. Safe from any goroutine. Tone
// sources report the zero value.
func (s *Source) JitterStats() buffers.JitterBufferStats {
	if s.jitter == nil {
		return buffers.JitterBufferStats{}
	}
	return s.jitter.Snapshot()
}

// readFrame fills one 240-sample frame on the audio thread and returns
// the pose of the frame's last sample. The jitter buffer's StreamReadPos
// is the reader's absolute position in the same written-stream domain the
// pose ring is keyed by, so alignment survives startup snaps, laps and
// underruns exactly.
func (s *Source) readFrame(dst []float32) (Pose, bool) {
	if s.tone != nil {
		s.tone.ReadMono(dst)
		return s.initialPose, true
	}
	s.jitter.Read(dst)
	L := uint64(s.jitter.StreamReadPos())
	if L == 0 {
		// Still warm-starting: nothing has played, hold the initial pose.
		return s.initialPose, true
	}
	if p, ok := s.ring.poseAt(L, &s.poseScratch); ok {
		return p, true
	}
	return s.initialPose, true
}
