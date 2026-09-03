package engine

import (
	"encoding/binary"
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
	decoder *inout.OpusInputDecoder // nil under SourceCodecRawF32
	tone    *inout.SineMonoInput
	// raw is SourceCodecRawF32: payloads are float32 samples, copied
	// into rawScratch (the payload aliases a datagram and need not be
	// 4-byte aligned).
	raw        bool
	rawScratch []float32

	ring        poseRing
	poseScratch [poseRingSize]poseSample // audio-thread scratch

	samplesWritten uint64 // writer-goroutine only: stream domain of the ring
	// group is set for a lockstep member: the shared buffer and the
	// member's channel in it. jitter is nil on a member.
	group        *SourceGroup
	groupIndex   int
	loudness     atomic.Uint32
	loudEMA      float32 // audio-thread-only EMA state
	initialPose  Pose
	decodeErrors atomic.Uint64
}

// WriteOpus ingests one packet: pose (nil = pose decimated) and Opus
// payload (empty = pose-only packet), with the gapless packet seq. Decode
// and jitter-write run here on the caller's goroutine.
func (s *Source) WriteOpus(seq uint64, pose *Pose, pkt []byte) {
	if s.tone != nil {
		return
	}
	if len(pkt) > 0 {
		s.ingestPCM(s.decodePacket(pkt))
	}
	if pose != nil {
		s.ring.push(seq, s.samplesWritten, *pose)
	}
}

// decodePacket turns one payload into this source's PCM, raw or Opus.
// A packet the decoder rejects is handled exactly as a lost one:
// concealed for a frame so the sample accounting (and so the pose
// ring's alignment) advances as the sender's timeline did, and counted
// for diagnostics. Never a panic — the bytes came from a client. The
// returned slice aliases decoder or scratch storage.
func (s *Source) decodePacket(pkt []byte) []float32 {
	if s.raw {
		return s.decodeRaw(pkt)
	}
	pcm, err := s.decoder.Decode(pkt)
	if err != nil {
		s.decodeErrors.Add(1)
		pcm = s.decoder.ConcealFloat32(FrameSize)
	}
	return pcm
}

// concealPCM produces samples of concealment for one lost frame:
// decoder PLC, or silence on the raw path.
func (s *Source) concealPCM(samples int) []float32 {
	if s.raw {
		n := min(samples, len(s.rawScratch))
		clear(s.rawScratch[:n])
		return s.rawScratch[:n]
	}
	return s.decoder.ConcealFloat32(samples)
}

// Group returns the lockstep set this source belongs to, or nil.
func (s *Source) Group() *SourceGroup { return s.group }

// DecodeErrors counts packets the Opus decoder rejected (each concealed
// as a lost frame). Safe from any goroutine.
func (s *Source) DecodeErrors() uint64 {
	return s.decodeErrors.Load()
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

// decodeRaw reads a SourceCodecRawF32 payload into rawScratch. A payload
// that is not a whole number of samples, or longer than a frame, counts
// as a decode error and is concealed as silence.
func (s *Source) decodeRaw(pkt []byte) []float32 {
	n := len(pkt) / 4
	if len(pkt)%4 != 0 || n > len(s.rawScratch) {
		s.decodeErrors.Add(1)
		clear(s.rawScratch)
		return s.rawScratch
	}
	for i := 0; i < n; i++ {
		s.rawScratch[i] = math.Float32frombits(binary.LittleEndian.Uint32(pkt[4*i:]))
	}
	return s.rawScratch[:n]
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
	if s.tone != nil {
		return
	}
	if s.raw {
		// No decoder state to advance: silence keeps the sample
		// accounting aligned.
		for samples > 0 {
			n := min(samples, len(s.rawScratch))
			clear(s.rawScratch[:n])
			s.jitter.Write(s.rawScratch[:n])
			s.samplesWritten += uint64(n)
			samples -= n
		}
		return
	}
	if s.decoder == nil {
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
	if s.group != nil {
		return s.group.LatencySamples()
	}
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
	if s.group != nil {
		return s.group.jitter.Snapshot()
	}
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
	var L uint64
	if g := s.group; g != nil {
		// The set's frame was read once in prep; take this member's
		// channel from it.
		g.channel(s.groupIndex, dst)
		L = g.readPos
	} else {
		s.jitter.Read(dst)
		L = uint64(s.jitter.StreamReadPos())
	}
	if L == 0 {
		// Still warm-starting: nothing has played, hold the initial pose.
		return s.initialPose, true
	}
	if p, ok := s.ring.poseAt(L, &s.poseScratch); ok {
		return p, true
	}
	return s.initialPose, true
}
