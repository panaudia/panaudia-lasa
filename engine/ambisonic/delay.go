package ambisonic

// Encode-side ITD (M4): Woodworth model delays, per-source shared history
// rings, per-(listener, ear) fractional read offsets — plan design
// decisions 2, 4 and 5.
//
// The model parameters (head radius, speed of sound) come from the loaded
// HRTF set's manifest via common.BilateralHeadRadiusM/SpeedOfSoundMS: the
// offline tool removed exactly these delays during ear-alignment
// (panaudia_hrtf/woodworth.py evaluates the identical closed form — golden
// values pinned in both repos' tests), so the runtime reinsertion sums back
// to the measured response by construction.

import (
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// DelayRingHistory is the per-source history kept ahead of the current
// frame in Encoder.inputRing: it must cover the largest per-ear read delay,
// delayBase + max(tau)/2. The PAHRTF loader rejects any set whose model
// delays exceed this ring (same closed form, shared constant — see
// common.DelayRingHistory); the initDelayModel panic is only a backstop
// against sets that bypassed the loader.
const DelayRingHistory = common.DelayRingHistory

// woodworthITDSamples returns the model ITD in samples for a head-frame
// lateral sine (the unit direction's Y component, +Y = left). Positive =
// left ear leads. tau = (a/c)(theta + sin theta), theta = arcsin(latSine) —
// so sin(theta) is the clamped input itself, no second transcendental.
func (encoder *Encoder) woodworthITDSamples(latSine float32) float32 {
	s := float64(latSine)
	if s > 1 {
		s = 1
	} else if s < -1 {
		s = -1
	}
	return float32((math.Asin(s) + s) * encoder.woodworthScale)
}

// initDelayModel wires the Woodworth parameters and the delay state at
// encoder construction (bilateral only). The base delay keeps every
// per-ear read causal: delay_ear = base -/+ tau/2 with base > max(tau)/2,
// a constant added to both ears (interaural relations exact, ~0.4 ms of
// fixed latency).
func (encoder *Encoder) initDelayModel() {
	a := common.BilateralHeadRadiusM
	c := common.BilateralSpeedOfSoundMS
	if a <= 0 || c <= 0 {
		panic("ambisonic: bilateral encoder needs the Woodworth delay model " +
			"(common.BilateralHeadRadiusM/SpeedOfSoundMS from the HRTF set's " +
			"manifest — the delay model and the filter set switch together)")
	}
	fs := float64(common.SAMPLE_RATE)
	encoder.woodworthScale = a / c * fs
	maxTau := a / c * (math.Pi/2 + 1) * fs
	encoder.delayBase = float32(math.Ceil(maxTau/2)) + 1
	if float64(encoder.delayBase)+maxTau/2 > DelayRingHistory {
		panic(fmt.Sprintf("ambisonic: head radius %.3f m needs %f samples of delay ring, have %d",
			a, float64(encoder.delayBase)+maxTau/2, DelayRingHistory))
	}
	encoder.prevDelayL = make([]float32, encoder.mixerConfig.MaxNodes)
	encoder.prevDelayR = make([]float32, encoder.mixerConfig.MaxNodes)
	encoder.slotGen = make([]uint64, encoder.mixerConfig.MaxNodes)
	encoder.slotOwner = make([]uuid.UUID, encoder.mixerConfig.MaxNodes)
	encoder.nfcState = make([][2][2 * nfcNumSections]float32, encoder.mixerConfig.MaxNodes)
	encoder.nfcGen = make([]uint64, encoder.mixerConfig.MaxNodes)
	// frameGen starts at 1 so the zero-value slotGen/nfcGen can never
	// satisfy the "packed last frame" check (== frameGen-1) on the first
	// rendered frame — the 0==0 collision ramped first-frame delays from
	// the zero-initialized prev arrays instead of snapping.
	encoder.frameGen = 1
}

// PushInputRing appends the current Input frame to the source's shared
// history ring. Called once per frame from the node's In step — before any
// listener render job runs (the In/Across barrier) — so every listener's
// fractional reads see the same stable ring (design decision 5).
func (encoder *Encoder) PushInputRing() {
	if encoder.inputRing == nil {
		return
	}
	frameSize := encoder.mixerConfig.FrameSize
	copy(encoder.inputRing[:DelayRingHistory], encoder.inputRing[frameSize:frameSize+DelayRingHistory])
	copy(encoder.inputRing[DelayRingHistory:DelayRingHistory+frameSize], encoder.Input)
}

// pairEarDelays returns this frame's per-ear read delays and the previous
// frame's (for the in-frame ramp) for one peer slot. A slot that was not
// packed last frame snaps (prev = current) instead of ramping from stale
// state — its weights are simultaneously fading in from zero, so the snap
// is inaudible. State reset needs no teardown hook: the generation check
// makes stale entries self-invalidating (slots ride the departure funnel's
// re-lease, design decision 5).
func (encoder *Encoder) pairEarDelays(slot int, latSine float32) (prevL, prevR, dL, dR float32) {
	tau := encoder.woodworthITDSamples(latSine)
	dL = encoder.delayBase - tau/2
	dR = encoder.delayBase + tau/2
	prevL, prevR = dL, dR
	if encoder.slotGen[slot] == encoder.frameGen-1 {
		prevL, prevR = encoder.prevDelayL[slot], encoder.prevDelayR[slot]
	}
	encoder.slotGen[slot] = encoder.frameGen
	encoder.prevDelayL[slot] = dL
	encoder.prevDelayR[slot] = dR
	return
}
