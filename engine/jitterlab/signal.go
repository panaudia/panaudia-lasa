package jitterlab

// signal.go — the self-describing ramp codec (plan/jitter-v4-harness.md).
// The writer emits, for stream frame position n, the sample value
// 1 + (n mod RampWrap) on every channel of the frame. float32 carries
// integers exactly up to 2^24, so values decode exactly; 0.0 is reserved
// for cleared output (silence); and the buffer's single-tap averaging
// splice produces exact half-integer values, so every splice
// self-identifies as a lone ".5" sample. The decoder classifies samples
// and unwraps the ~21.8 s (at 48 kHz) wrap by proximity: legitimate jumps
// (snaps) are bounded by ring capacity, far below RampWrap/2.

import "math"

// RampWrap is the ramp modulus. Positions encode as 1..RampWrap; the wrap
// period (2^20 frames ≈ 21.8 s at 48 kHz) vastly exceeds any legitimate
// stream-position jump, making proximity unwrapping unambiguous.
const RampWrap = int64(1) << 20

// fillRamp writes nFrames frames of ramp signal starting at stream frame
// position pos into dst (interleaved, nc channels; every channel of a
// frame carries the frame's value — corrections move whole frames, so
// channel 0 suffices for decoding).
func fillRamp(dst []float32, pos int64, nFrames int64, nc int) {
	for f := int64(0); f < nFrames; f++ {
		v := float32(1 + (pos+f)%RampWrap)
		base := f * int64(nc)
		for ch := int64(0); ch < int64(nc); ch++ {
			dst[base+ch] = v
		}
	}
}

type sampleKind uint8

const (
	sampSilence sampleKind = iota // 0.0 — cleared output
	sampPlain                     // integer in [1, RampWrap] — a stream position
	sampMarker                    // half-integer — a splice boundary
	sampAlien                     // anything else — not producible by ramp + v3 splice
)

// classifySample decodes one sample. For sampPlain the second return is
// the wrapped stream position in [0, RampWrap).
func classifySample(v float32) (sampleKind, int64) {
	if v == 0 {
		return sampSilence, 0
	}
	f := float64(v)
	i, frac := math.Modf(f)
	switch frac {
	case 0:
		w := int64(i)
		if w >= 1 && w <= RampWrap {
			return sampPlain, w - 1
		}
		return sampAlien, 0
	case 0.5:
		return sampMarker, 0
	default:
		return sampAlien, 0
	}
}

// markerValue returns the exact splice-marker sample the buffer's
// single-tap average produces for adjacent stream positions p, p+1. All
// quantities are exactly representable in float32, so observed markers
// compare bit-exactly against expected ones.
func markerValue(p int64) float32 {
	a := float32(1 + p%RampWrap)
	b := float32(1 + (p+1)%RampWrap)
	return (a + b) * 0.5
}

// unwrapPos maps a wrapped position w to the absolute position congruent
// to w (mod RampWrap) nearest to lastAbs+1 (the expected next frame).
func unwrapPos(lastAbs, w int64) int64 {
	next := lastAbs + 1
	d := (w - next%RampWrap + RampWrap) % RampWrap
	if d > RampWrap/2 {
		d -= RampWrap
	}
	return next + d
}
