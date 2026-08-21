package ambisonic

// Near-field compensation (M6, plan design decision 3): for sources inside
// the gate distance, each ear's packed input row is additionally shaped by
// two cascaded biquads whose coefficients come from the generated
// Duda-Martens difference-filter table (nfc_table_gen.go — built, gated
// and stability-checked offline by ../panaudia-hrtf).
//
// Lookup is bilinear over (1/rho, theta): rho = distance / ear offset (the
// table is head-radius generic), theta = angle between the ear ray and the
// source ray (the right ear reads the same table with its mirrored theta).
// Distances below the table's nearest row clamp to it — the existing
// inside-head clamp semantics; the near-field gain boost is bounded by
// that same row (the offline model caps at rho = 1.5).
//
// The gate blends the coefficients linearly toward identity across
// [NFCGateFullM, NFCGateOffM] and skips processing entirely beyond it, so
// the bypass region costs nothing and the blend-in is click-free (at the
// boundary the blended filter IS identity and the biquad state is zero).

import (
	"math"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

const (
	// NFCGateOffM matches ParallaxFarFieldM: inside 2 m a source gets
	// per-ear directions AND near-field shaping; beyond it neither.
	NFCGateOffM  = ParallaxFarFieldM
	NFCGateFullM = 1.8
)

// nfcIdentity is the passthrough coefficient set the gate blends toward.
var nfcIdentity = [nfcNumSections][5]float32{{1, 0, 0, 0, 0}, {1, 0, 0, 0, 0}}

// nfcCoefficients fills c for one ear. cosTheta = ear-ray·source-ray
// (head frame: +latSine for the left ear, -latSine for the right).
// Returns false (c untouched) when the source is outside the gate.
// The render loop uses nfcCoefficientsPair instead — same parts, one setup.
func nfcCoefficients(distMeters float64, cosTheta float32, c *[nfcNumSections][5]float32) bool {
	gate, i0, fu, ok := nfcSetup(distMeters)
	if !ok {
		return false
	}
	nfcFill(gate, i0, fu, nfcThetaDeg(cosTheta), c)
	return true
}

// nfcCoefficientsPair fills both ears' coefficient sets in one pass: the
// gate and the 1/rho row interpolation are ear-independent, and the right
// ear's angle is the left's mirror (acos(−x) = 180° − acos(x)) — one acos
// and one setup instead of two.
func nfcCoefficientsPair(distMeters float64, latSine float32, cL, cR *[nfcNumSections][5]float32) bool {
	gate, i0, fu, ok := nfcSetup(distMeters)
	if !ok {
		return false
	}
	thetaDeg := nfcThetaDeg(latSine)
	nfcFill(gate, i0, fu, thetaDeg, cL)
	nfcFill(gate, i0, fu, 180.0-thetaDeg, cR)
	return true
}

// nfcSetup computes the ear-independent lookup half: the gate blend and
// the 1/rho row interpolation. ok is false outside the gate.
func nfcSetup(distMeters float64) (gate float32, i0 int, fu float32, ok bool) {
	if distMeters >= NFCGateOffM {
		return 0, 0, 0, false
	}
	gate = float32((NFCGateOffM - distMeters) / (NFCGateOffM - NFCGateFullM))
	if gate > 1 {
		gate = 1
	}

	invRho := common.BilateralEarOffsetM / distMeters
	if invRho > nfcInvRhoMax {
		invRho = nfcInvRhoMax // inside-head clamp: nearest table row
	} else if invRho < nfcInvRhoMin {
		invRho = nfcInvRhoMin
	}
	u := (invRho - nfcInvRhoMin) / (nfcInvRhoMax - nfcInvRhoMin) * float64(nfcNumRho-1)
	i0 = int(u)
	if i0 > nfcNumRho-2 {
		i0 = nfcNumRho - 2
	}
	return gate, i0, float32(u - float64(i0)), true
}

func nfcThetaDeg(cosTheta float32) float64 {
	ct := float64(cosTheta)
	if ct > 1 {
		ct = 1
	} else if ct < -1 {
		ct = -1
	}
	return math.Acos(ct) * (180.0 / math.Pi)
}

// nfcFill bilinearly interpolates the table at (i0+fu, thetaDeg) and blends
// toward identity by the gate.
func nfcFill(gate float32, i0 int, fu float32, thetaDeg float64, c *[nfcNumSections][5]float32) {
	v := thetaDeg / nfcThetaStep
	j0 := int(v)
	if j0 > nfcNumTheta-2 {
		j0 = nfcNumTheta - 2
	}
	fv := float32(v - float64(j0))

	c00 := &nfcTable[i0][j0]
	c10 := &nfcTable[i0+1][j0]
	c01 := &nfcTable[i0][j0+1]
	c11 := &nfcTable[i0+1][j0+1]
	for s := 0; s < nfcNumSections; s++ {
		for k := 0; k < 5; k++ {
			lo := c00[s][k] + fu*(c10[s][k]-c00[s][k])
			hi := c01[s][k] + fu*(c11[s][k]-c01[s][k])
			fit := lo + fv*(hi-lo)
			id := nfcIdentity[s][k]
			c[s][k] = id + gate*(fit-id)
		}
	}
}
