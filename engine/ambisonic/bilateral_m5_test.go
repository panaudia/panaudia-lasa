package ambisonic

// M5 verification (plan milestone M5): near lateral sources → the per-ear
// azimuths straddle the head-centre azimuth; far sources → the two buses
// share one weight vector exactly.

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

func azimuthDeg(p common.Position) float64 {
	return math.Atan2(p.Y, p.X) * 180 / math.Pi
}

func TestParallaxDirectionsStraddle(t *testing.T) {
	measRadius := 3.25 // KU100
	earOffset := 0.0875

	// Near source front-left at 0.5 m, az 45: the closer (left) ear sees it
	// more FRONTAL (the +Y offset toward the source shrinks the lateral
	// component of the ear→source vector), the far (right) ear more
	// lateral — the same parallax as viewing a nearby object with each eye.
	rel := common.Position{X: 0.5 * math.Cos(math.Pi/4), Y: 0.5 * math.Sin(math.Pi/4), Z: 0}
	azC := azimuthDeg(rel)
	dirL := earProjectedDirection(rel, +earOffset, measRadius)
	dirR := earProjectedDirection(rel, -earOffset, measRadius)
	azL, azR := azimuthDeg(dirL), azimuthDeg(dirR)
	if !(azL < azC && azC < azR) {
		t.Fatalf("straddle violated: azL %.2f, azC %.2f, azR %.2f", azL, azC, azR)
	}
	// At 0.5 m with an 8.75 cm ear offset the split is several degrees.
	if azR-azL < 5 {
		t.Fatalf("parallax split %.2f° implausibly small at 0.5 m", azR-azL)
	}

	// Horizontal source stays horizontal per ear.
	if math.Abs(dirL.Z) > 1e-12 || math.Abs(dirR.Z) > 1e-12 {
		t.Fatalf("horizontal source left the horizontal plane: %g %g", dirL.Z, dirR.Z)
	}

	// The projection's fixed point: a source AT the measurement radius maps
	// to its own direction for both ears (the measured set already encodes
	// the ears' geometry for sources on that sphere) — zero split.
	relAtR := common.Position{X: measRadius * math.Cos(math.Pi/4), Y: measRadius * math.Sin(math.Pi/4), Z: 0}
	dLr := azimuthDeg(earProjectedDirection(relAtR, +earOffset, measRadius))
	dRr := azimuthDeg(earProjectedDirection(relAtR, -earOffset, measRadius))
	if math.Abs(dLr-azimuthDeg(relAtR)) > 1e-9 || math.Abs(dRr-azimuthDeg(relAtR)) > 1e-9 {
		t.Fatalf("projection not fixed at the measurement radius: %.4f / %.4f vs %.4f",
			dLr, dRr, azimuthDeg(relAtR))
	}

	// Far source: the projected split does NOT vanish at infinity — it
	// approaches ~earOffset/measRadius per ear (~1.8° total here), which is
	// far below order-3 angular resolution. That asymptote is exactly why
	// the >= 2 m short-circuit shares one SH evaluation instead (tested in
	// TestParallaxWeightSharing); assert the asymptote stays small and well
	// under the near-field split.
	relFar := common.Position{X: 20 * math.Cos(math.Pi/4), Y: 20 * math.Sin(math.Pi/4), Z: 0}
	dL := azimuthDeg(earProjectedDirection(relFar, +earOffset, measRadius))
	dR := azimuthDeg(earProjectedDirection(relFar, -earOffset, measRadius))
	if s := math.Abs(dL - dR); s > 2.5 || s > (azR-azL)/2 {
		t.Fatalf("far-field split %.3f° out of expected range (near split %.3f°)", s, azR-azL)
	}

	// Unknown measurement radius: the radius→∞ limit is the plain
	// from-the-ear direction, which must still straddle the same way.
	dirL0 := earProjectedDirection(rel, +earOffset, 0)
	dirR0 := earProjectedDirection(rel, -earOffset, 0)
	if !(azimuthDeg(dirL0) < azC && azC < azimuthDeg(dirR0)) {
		t.Fatal("straddle violated with unknown measurement radius")
	}
}

func TestParallaxWeightSharing(t *testing.T) {
	setTestDelayModel(t)
	prevR := common.BilateralMeasurementRadiusM
	common.BilateralMeasurementRadiusM = 3.25
	defer func() { common.BilateralMeasurementRadiusM = prevR }()

	cfg := m1Config()
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})

	weightsL := make([]float32, cfg.ChannelCount)
	weightsR := make([]float32, cfg.ChannelCount)

	// Far source (5 m): both buses share one SH evaluation, bit-identical.
	far := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 1)
	far.SetPosition(common.Position{X: 0.85, Y: 0.85, Z: 0.5})
	listener.bilateralWeights(far, nil, weightsL, weightsR, nil)
	for i := range weightsL {
		if weightsL[i] != weightsR[i] {
			t.Fatalf("far source: weights differ at %d: %v vs %v", i, weightsL[i], weightsR[i])
		}
	}

	// Near lateral source (1 m, front-left): the ears' weight vectors must
	// genuinely differ.
	near := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 2)
	near.SetPosition(common.Position{X: 0.57, Y: 0.57, Z: 0.5})
	listener.bilateralWeights(near, nil, weightsL, weightsR, nil)
	var diff, energy float64
	for i := range weightsL {
		d := float64(weightsL[i] - weightsR[i])
		diff += d * d
		energy += float64(weightsL[i]) * float64(weightsL[i])
	}
	if diff == 0 {
		t.Fatal("near source: per-ear weights identical — parallax inactive")
	}
	if rel := math.Sqrt(diff / energy); rel < 0.01 {
		t.Fatalf("near source: relative weight difference %.4f implausibly small", rel)
	}

	// Median-plane near source: mirror symmetry — the two ears see it at
	// mirrored azimuths, so W (ACN 0) matches and Y (ACN 1) is antisymmetric.
	mid := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 3)
	mid.SetPosition(common.Position{X: 0.58, Y: 0.5, Z: 0.5})
	listener.bilateralWeights(mid, nil, weightsL, weightsR, nil)
	if weightsL[0] != weightsR[0] {
		t.Fatalf("median-plane near: W weights differ: %v vs %v", weightsL[0], weightsR[0])
	}
	if math.Abs(float64(weightsL[1]+weightsR[1])) > 1e-6 || weightsL[1] == 0 {
		t.Fatalf("median-plane near: Y weights not antisymmetric: %v vs %v", weightsL[1], weightsR[1])
	}
}
