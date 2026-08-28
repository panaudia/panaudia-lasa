package engine

import (
	"time"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// Reverb presets, re-exported so hosts never import the DSP layer.
const (
	ReverbNone       = common.REVERB_NONE
	ReverbTightRoom  = common.REVERB_TIGHT_ROOM
	ReverbSmallRoom  = common.REVERB_SMALL_ROOM
	ReverbMediumRoom = common.REVERB_MEDIUM_ROOM
	ReverbLargeHall  = common.REVERB_LARGE_HALL
	ReverbCathedral  = common.REVERB_CATHEDRAL
)

// FrameSize and SampleRate are the engine's hard invariants (48 kHz,
// 240-sample / 5 ms frames), inherited from the DSP layer.
const (
	FrameSize  = common.FRAME_SIZE
	SampleRate = common.SAMPLE_RATE
)

// Config configures a Mixer. The zero value is not useful — start from
// DefaultConfig.
type Config struct {
	// Order is the ambisonic order of the internal buses (2–5; every
	// stage — SH evaluation, pack/GEMM, HRTF decode — is order-generic
	// and the default HRTF set carries banks for all four).
	Order int
	// MaxEntities bounds concurrent entities; slot-indexed buffers are
	// sized by it at construction.
	MaxEntities int
	// ReverbPreset is one of the Reverb* constants.
	ReverbPreset int
	// Workers is the number of render worker goroutines.
	Workers int
	// PureGoMixer forces the pure-Go gonum mixing path over the GEMM
	// backend (A/B hatch; output is identical, only speed differs).
	PureGoMixer bool
}

func DefaultConfig() Config {
	return Config{Order: 3, MaxEntities: 64, ReverbPreset: ReverbMediumRoom, Workers: 16}
}

// RenderParams is the render-relevant per-entity state that crosses the
// engine boundary — the shape-(C) per-entity tier. The engine owns the
// defaults (an entity added before its state arrives must render
// sensibly); the host maps its own vocabulary (e.g. the LASA base
// profile's lasa.entity.* keys) onto this struct and sets it wholesale,
// writing DefaultRenderParams fields back on clears. Values are taken
// literally: a nil or empty channel list means member of no channels.
type RenderParams struct {
	// Gain is the source gain multiplier.
	Gain float64
	// Attenuation is the distance-attenuation exponent (0 = same level
	// everywhere, 2 = inverse-square).
	Attenuation float64
	// Size is the source size in metres; 0 is a point source. Plumbed,
	// not yet rendered.
	Size float64
	// Directivity is 0 (omnidirectional) to 1 (maximally directional).
	// Plumbed, not yet rendered.
	Directivity float64
	// SourceChannels and SinkChannels are the entity's channel
	// memberships: a source is audible to a sink iff they share at least
	// one channel that is not muted.
	SourceChannels []string
	SinkChannels   []string
}

// DefaultRenderParams mirrors the LASA base profile's documented
// absence-means-default values; a parity test in the server asserts the
// two cannot drift.
func DefaultRenderParams() RenderParams {
	return RenderParams{
		Gain:           1.0,
		Attenuation:    2.0,
		Size:           0.0,
		Directivity:    0.0,
		SourceChannels: []string{"main"},
		SinkChannels:   []string{"main"},
	}
}

// ChannelParams is the space-scoped per-channel tier. Channels exist by
// being named; an unnamed channel behaves as DefaultChannelParams.
//
// Semantics (base profile §4, pinned 2026-07-30; applied by the pair
// record since 2026-08-04): across a pair's shared unmuted channels the
// highest gain wins (unset = identity 1.0, so storing 1.0 is exactly
// "unset") and the lowest SET attenuation wins; the winning gain then
// MULTIPLIES the entity's render gain (amplitude domain), and the
// winning attenuation REPLACES the entity's attenuation only when
// explicitly set — nil Attenuation is "no override" (a channel with no
// attenuation set imposes no distance-law policy).
type ChannelParams struct {
	// Muted removes the channel from every audibility intersection.
	Muted       bool
	Gain        float64
	Attenuation *float64
}

func DefaultChannelParams() ChannelParams {
	return ChannelParams{Muted: false, Gain: 1.0, Attenuation: nil}
}

// SourceConfig configures AddSource.
// SourceCodec selects how a source's WriteOpus payloads are decoded.
// The zero value is production Opus.
type SourceCodec int

const (
	// SourceCodecOpus decodes mono Opus at 48 kHz (production).
	SourceCodecOpus SourceCodec = iota
	// SourceCodecRawF32 takes each payload as little-endian float32
	// mono samples and conceals loss with silence. No codec in the
	// path: for capacity harnesses that price the server pipeline
	// without a decoder in it. Never a wire format.
	SourceCodecRawF32
)

// SinkCodec selects a binaural sink's egress encoder. The zero value is
// production Opus.
type SinkCodec int

const (
	// SinkCodecOpus encodes the binaural frame as coupled stereo Opus
	// at BinauralBitrate (production).
	SinkCodecOpus SinkCodec = iota
	// SinkCodecRawF32 hands the binaural frame on as interleaved
	// float32 bytes: the full decode and dynamics, no codec. For
	// capacity harnesses; never a wire format.
	SinkCodecRawF32
)

type SourceConfig struct {
	// Codec is how WriteOpus payloads are decoded (default Opus).
	Codec SourceCodec
	// InitialPose positions the source until the first pose arrives.
	InitialPose Pose
	// InputChannels is the Opus decode channel count for WriteOpus:
	// 1 (default) or 2 (stereo is downmixed to mono after decode).
	InputChannels int
	// Jitter geometry: the writer's packet duration and the reader
	// cadence. Zero values take the defaults (5 ms / 5 ms — the LASA
	// wire framing).
	JitterWriterFrame time.Duration
	JitterReaderFrame time.Duration
	// Quality is the LASA stream quality level 0–2 (entity definition
	// `quality`, lasa-core.md §4.2): the stream's latency-tolerance
	// INTENT, mapped onto the v4 jitter buffer's {floor, robustness,
	// capacity} by buffers.QualityLevel. 0 = interactive (default).
	// The v4 buffer measures jitter widths itself, so the v3 era's
	// per-route hand-tuned window bounds are gone (design doc §8;
	// their environments are acceptance criteria A3–A5).
	Quality int
	// Redundancy is the entity's declared maximum LASA redundancy
	// offset in packets (entity definition `redundancy`, lasa-core.md
	// §4.2/§5.1; 0 = none). It sets the FEC latency floor via
	// buffers.FECFloor — a redundant copy repairs a loss only if the
	// buffer already spans the offset, so the floor is provisioned
	// here, at admission, never learned from a click.
	Redundancy int
	// TestTone, when > 0, makes the source a synthetic sine of that
	// frequency (Hz): Write calls are ignored and the pose stays at
	// InitialPose. For tests and diagnostics only.
	TestTone float64
	// HeadFrame pins the source relative to EVERY listener's head (the
	// aural HUD — LASA base profile §6 frame: "head"): its poses are
	// head-relative offsets, consumed verbatim by the per-pair geometry
	// (no world translation, no head rotation). Head-frame sources ride
	// the same jitter-aligned pose pipeline as world sources (decided
	// 2026-08-05) and are EXCLUDED from raw-ambisonic (classic) renders
	// — a world-frame field cannot carry a per-listener source.
	HeadFrame bool
}

func (c *SourceConfig) applyDefaults() {
	if c.InputChannels == 0 {
		c.InputChannels = 1
	}
	if c.JitterWriterFrame == 0 {
		c.JitterWriterFrame = 5 * time.Millisecond
	}
	if c.JitterReaderFrame == 0 {
		c.JitterReaderFrame = 5 * time.Millisecond
	}
}
