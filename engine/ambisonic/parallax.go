package ambisonic

// Per-ear parallax directions (M5, plan milestone M5): for near sources the
// two ears see the source from measurably different directions, so each ear
// bus gets its own SH weight vector. Directions come from one per-pair
// geometry pass — never two independent GetWeights calls.
//
// Geometry (head frame, +Y = left): each ear sits at ±BilateralEarOffsetM
// on the interaural axis — the GEOMETRIC offset, not the Woodworth-fit
// radius (that one is an ITD-estimator fit and overstates the physical
// head). The ear→source ray is projected onto the sphere of the HRTF
// measurement radius centred on the head (BRT GetSphereProjectionPosition
// is the reference): the direction under which the measured set best
// represents that ray. With no recorded radius the projection degrades to
// the plain from-the-ear direction (its radius→∞ limit).

import (
	"math"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// ParallaxFarFieldM is the far-field short-circuit distance: beyond it the
// per-ear directions converge (angular split ≤ ~2.5°) and both buses share
// one SH evaluation. Packed input rows are NEVER shared — ITD applies at
// all distances.
const ParallaxFarFieldM = 2.0

// earProjectedDirection returns the unit direction for one ear: rel is the
// head-frame source vector (meters), earY the ear's signed offset on the
// interaural axis, measRadius the HRTF measurement radius (0 = unknown).
func earProjectedDirection(rel common.Position, earY, measRadius float64) common.Position {
	// Ray origin at the ear, toward the source.
	v := common.Position{X: rel.X, Y: rel.Y - earY, Z: rel.Z}
	vLen := math.Sqrt(v.X*v.X + v.Y*v.Y + v.Z*v.Z)
	if vLen < 1e-10 {
		// Source at the ear itself: fall back to the interaural axis.
		return common.Position{X: 0, Y: math.Copysign(1, earY), Z: 0}
	}
	v = common.Position{X: v.X / vLen, Y: v.Y / vLen, Z: v.Z / vLen}
	if measRadius <= 0 {
		return v
	}
	// Intersect ear + t·v with the measurement sphere |p| = R:
	// t = -(e·v) + sqrt((e·v)^2 + R^2 - |e|^2); |e| << R keeps it real.
	ev := earY * v.Y
	disc := ev*ev + measRadius*measRadius - earY*earY
	t := -ev + math.Sqrt(disc)
	p := common.Position{X: t * v.X, Y: earY + t*v.Y, Z: t * v.Z}
	return common.Position{X: p.X / measRadius, Y: p.Y / measRadius, Z: p.Z / measRadius}
}

// bilateralWeights is the flag-on per-pair weight pass: one geometry
// evaluation emitting both ears' dry weight vectors (per-ear directions
// inside ParallaxFarFieldM, one shared SH evaluation beyond it), optionally
// the wet reverb weights (head-centre direction — the late field is
// diffuse), and the lateral sine for the Woodworth delay (head-centre, the
// alignment-consistency direction). Also returns the head-centre distance
// in meters (the M6 NFC gate/lookup input).
func (encoder *Encoder) bilateralWeights(peer *Encoder,
	headFrame *common.RotationMatrix33,
	weightsL, weightsR, wetWeights []float32) (float32, float64) {

	var rel common.Position
	if peer.headFrameSource {
		// Head-frame source (the aural HUD): its pose IS the
		// head-relative vector — no world translation and, below, no
		// head rotation. Everything downstream (per-ear parallax, ITD,
		// NFC, extent) operates on it unchanged, so a whisper-range
		// angel gets the full near-field treatment.
		rel = peer.PositionMeters
	} else {
		rel = common.TrigCartesianRelativePosition(encoder.ListenerPositionMeters(), peer.PositionMeters)
	}
	dist := math.Sqrt(rel.X*rel.X + rel.Y*rel.Y + rel.Z*rel.Z)
	if dist < 1e-10 {
		// Coincident positions: same degenerate direction as pairGeometry.
		rel, dist = common.Position{X: 0, Y: 1, Z: 0}, 1e-10
	}

	// Extent/directivity laws use the WORLD-frame ray (the source's
	// forward is world-frame); the surface clamp feeds attenuation and
	// the NFC gate below (size is diffuseness — inside a large source
	// approximates to enveloping, never a centre gain pump). Gated so
	// neutral sources — the common case — pay nothing
	// (TestExtentBudgetAtScale proves the no-op).
	pairGain, pairAtt := encoder.pairScalars(peer)
	dAtt := dist
	extentGain := float32(1)
	var ow [spreadMaxOrder + 1]float32
	hasSpread := false
	if peer.size > 0 || peer.directivity > 0 {
		dAtt = surfaceDistance(dist, peer.size)
		extentGain = directivityFactor(peer.forward, rel, dist, peer.directivity)
		hasSpread = spreadOrderWeights(peer.size, dist, encoder.mixerConfig.Order, &ow)
	}
	nodeGain := pairGain * distanceGain(dAtt, pairAtt) * extentGain

	if headFrame != nil && !peer.headFrameSource {
		rel = headFrame.Apply(rel)
	}
	norm := common.Position{X: rel.X / dist, Y: rel.Y / dist, Z: rel.Z / dist}

	order := encoder.mixerConfig.Order
	farField := dist >= ParallaxFarFieldM
	if farField {
		// Far field: one SH evaluation shared by both ears.
		GetSphericalHarmonicsGained(order, float32(norm.X), float32(norm.Y), float32(norm.Z), nodeGain, weightsL)
		if hasSpread {
			applyOrderWeights(order, &ow, weightsL)
		}
		copy(weightsR, weightsL)
	} else {
		earOffset := common.BilateralEarOffsetM
		measRadius := common.BilateralMeasurementRadiusM
		dirL := earProjectedDirection(rel, +earOffset, measRadius)
		dirR := earProjectedDirection(rel, -earOffset, measRadius)
		GetSphericalHarmonicsGained(order, float32(dirL.X), float32(dirL.Y), float32(dirL.Z), nodeGain, weightsL)
		GetSphericalHarmonicsGained(order, float32(dirR.X), float32(dirR.Y), float32(dirR.Z), nodeGain, weightsR)
		if hasSpread {
			applyOrderWeights(order, &ow, weightsL)
			applyOrderWeights(order, &ow, weightsR)
		}
	}

	if wetWeights != nil {
		// Same dry/wet split as GetWeightsForReverb (reverbSplit), per ear:
		// the wet weights follow the head-centre direction; the dry gain
		// fold applies to the reverb-carrying channels of both ear buses.
		dryGain, wetGain := reverbSplit(dist, peer.size)

		if farField {
			// weightsL already holds nodeGain × head-centre SH — reuse it
			// before the dry fold instead of re-evaluating.
			for i := 0; i < common.REVERB_CHANNELS; i++ {
				wetWeights[i] = weightsL[i] * wetGain
				weightsL[i] *= dryGain
				weightsR[i] *= dryGain
			}
		} else {
			// Near field: the ear-projected dry weights are off head-centre,
			// so the wet weights need their own evaluation — but only the
			// order-1 channels the reverb bus carries.
			GetSphericalHarmonics(1, float32(norm.X), float32(norm.Y), float32(norm.Z), encoder.sphericalHarmonics)
			if hasSpread {
				applyOrderWeights(1, &ow, encoder.sphericalHarmonics[:common.REVERB_CHANNELS])
			}
			for i := 0; i < common.REVERB_CHANNELS; i++ {
				wetWeights[i] = encoder.sphericalHarmonics[i] * nodeGain * wetGain
				weightsL[i] *= dryGain
				weightsR[i] *= dryGain
			}
		}
	}

	// The clamped distance also gates NFC: no near-field bass boost
	// inside a large diffuse source.
	return float32(norm.Y), dAtt
}
