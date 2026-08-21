package ambisonic

// Regression tests for the generation-tracking family (review findings
// 6/7/8, plan/near-field-compensation/review-findings-m1-m8.md): per-slot
// delay/NFC state must be invalidated on slot re-lease and across
// empty-render gaps, and must never ramp from zero-value state on the
// first rendered frame.

import (
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// nearLateralSource builds a source close enough to the listener for the
// NFC biquads to run (inside the gate) and lateral enough for a real ITD.
func nearLateralSource(cfg common.MixerConfig, slot int, y float64) *Encoder {
	src := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, slot)
	src.SetPosition(common.Position{X: 0.5, Y: y, Z: 0.5})
	return src
}

// renderNoiseFrames drives the listener for n frames of noise from src,
// leaving nonzero NFC biquad state behind.
func renderNoiseFrames(listener, src *Encoder, mixer, mixerR, reverbMixer *Mixer, rng *rand.Rand, n int) {
	for frame := 0; frame < n; frame++ {
		for j := range src.Input {
			src.Input[j] = rng.Float32()*2 - 1
		}
		src.PushInputRing()
		listener.EncodePeers([]*Encoder{src}, mixer, mixerR, reverbMixer)
	}
}

// Finding 6: a source departing and a new one re-leasing its slot within
// adjacent frames must not inherit the departed source's NFC biquad tail
// or delay-ramp start. The new source is near (NFC active) but SILENT:
// with fresh state its contribution is exactly zero; inherited state
// leaks a decaying nonzero tail.
func TestSlotReleaseDoesNotInheritState(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	mixer, mixerR, reverbMixer := newTestMixers(cfg)

	const slot = 3
	a := nearLateralSource(cfg, slot, 0.55)
	renderNoiseFrames(listener, a, mixer, mixerR, reverbMixer, rand.New(rand.NewSource(9)), 4)

	b := nearLateralSource(cfg, slot, 0.45)
	b.PushInputRing() // Input is zero-valued: a silent frame on a fresh ring
	listener.EncodePeers([]*Encoder{b}, mixer, mixerR, reverbMixer)

	for i, v := range listener.Output {
		if v != 0 {
			t.Fatalf("sample %d: %v — departed source's state colored the re-leased slot", i, v)
		}
	}
}

// Finding 7: frames where the listener renders zero peers must advance the
// frame generation, so state laid down before the gap reads as stale on
// resume. The source goes silent during the gap (ring fully flushed after
// two pushes), so a correct resume renders exactly zero; the frozen-
// generation bug kept the pre-gap NFC tail alive.
func TestEmptyFrameGapInvalidatesState(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	mixer, mixerR, reverbMixer := newTestMixers(cfg)

	const slot = 5
	a := nearLateralSource(cfg, slot, 0.55)
	renderNoiseFrames(listener, a, mixer, mixerR, reverbMixer, rand.New(rand.NewSource(11)), 4)

	for frame := 0; frame < 3; frame++ {
		clear(a.Input)
		a.PushInputRing()
		listener.EncodePeers(nil, mixer, mixerR, reverbMixer)
	}

	listener.EncodePeers([]*Encoder{a}, mixer, mixerR, reverbMixer)
	for i, v := range listener.Output {
		if v != 0 {
			t.Fatalf("sample %d: %v — pre-gap state survived an empty-render gap", i, v)
		}
	}
}

// Finding 8: on a fresh listener's very first rendered frame the per-ear
// delays must snap (prev == current), not ramp from the zero-initialized
// prev arrays — frameGen starts at 1 precisely so the zero-value slotGen
// can never read as "packed last frame".
func TestFirstFrameSnapsDelays(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)

	listener.frameGen++ // what EncodePeers does at the top of frame 1
	prevL, prevR, dL, dR := listener.pairEarDelays(2, 0.7)
	if prevL != dL || prevR != dR {
		t.Fatalf("first frame ramps from %v/%v instead of snapping to %v/%v",
			prevL, prevR, dL, dR)
	}
	if prevL == 0 || prevR == 0 {
		t.Fatal("first-frame delays snapped to zero — delayBase missing")
	}
}
