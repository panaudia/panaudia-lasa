package ambisonic

// Source extent and directivity laws (engine plan/size-and-directivity.md,
// 2026-08-04) — shared by the bilateral (bilateralWeights) and classic
// (pairGeometry/GetWeights*) render paths so the two cannot drift, like
// distanceGain and reverbSplit.
//
// Directivity: the cardioid family g(θ) = (1−k) + k·cosθ, θ between the
// source's facing (+X, world frame) and the source→listener ray —
// broadband only (the Steam Audio / Resonance model).
//
// Size: per-order SH spread weighting. The apparent angular radius
// α = atan(size/2d) selects spherical-cap window weights per SH order
// (closed-form Legendre cap integration, tabulated at init and lerped
// per pair), energy-renormalised with a bounded W boost; at full spread
// only the omni component remains and the source envelops the listener.
// The surface-distance clamp attenuates by distance to the surface
// (d floored at size/2): size is an alias for source diffuseness, and
// "inside" approximates to "all around you" rather than a gain pump at
// the centre (decided 2026-08-04).

import (
	"math"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// surfaceDistance floors the pair distance at the source's surface.
func surfaceDistance(dist, size float64) float64 {
	if half := size / 2; dist < half {
		return half
	}
	return dist
}

// directivityFactor evaluates the cardioid-family gain for a source
// facing `forward` (unit, world frame) toward a listener along the
// world-frame listener→source direction rel (unnormalised; dist = |rel|).
// k ≤ 0 or degenerate geometry is unity.
func directivityFactor(forward common.Position, rel common.Position, dist, k float64) float32 {
	if k <= 0 || dist < 1e-9 {
		return 1
	}
	// source→listener = −rel; cosθ = forward · (−rel) / dist.
	cos := -(forward.X*rel.X + forward.Y*rel.Y + forward.Z*rel.Z) / dist
	g := (1 - k) + k*cos
	if g < 0 {
		g = 0
	}
	return float32(g)
}

// --- spread weighting ----------------------------------------------------

const (
	spreadMaxOrder = 5  // the SH evaluators go to order 5
	spreadSamples  = 64 // table resolution over α ∈ [0, π]
	// spreadNormCap bounds the energy-renormalisation boost of the
	// surviving low orders (at full spread the omni channel would take
	// (order+1)× to preserve SH-domain energy; cap it at +6 dB). THE
	// tuning knob for perceived level walking into a large source.
	spreadNormCap = 2.0
)

// spreadTable[i][l]: cap-window weight for order l at cap half-angle
// α = i/spreadSamples · π. Row 0 is all ones (point source); the last
// row is the full sphere (omni only).
var spreadTable [spreadSamples + 1][spreadMaxOrder + 1]float64

func init() {
	for i := 0; i <= spreadSamples; i++ {
		alpha := float64(i) / spreadSamples * math.Pi
		x := math.Cos(alpha)
		if i == 0 {
			for l := 0; l <= spreadMaxOrder; l++ {
				spreadTable[i][l] = 1
			}
			continue
		}
		// Spherical-cap window: c_l = (P_{l−1}(x) − P_{l+1}(x)) /
		// ((2l+1)(1−x)); c_0 ≡ 1, so the weights are already relative
		// to the omni channel.
		for l := 0; l <= spreadMaxOrder; l++ {
			var pm1 float64 = 1
			if l > 0 {
				pm1 = legendreP(l-1, x)
			}
			spreadTable[i][l] = (pm1 - legendreP(l+1, x)) / (float64(2*l+1) * (1 - x))
		}
	}
}

// legendreP evaluates the Legendre polynomial P_n(x) by recurrence
// (init-time only).
func legendreP(n int, x float64) float64 {
	p0, p1 := 1.0, x
	if n == 0 {
		return p0
	}
	for l := 2; l <= n; l++ {
		p0, p1 = p1, (float64(2*l-1)*x*p1-float64(l-1)*p0)/float64(l)
	}
	return p1
}

// spreadOrderWeights fills ow[0..order] with the energy-renormalised
// per-order weights for a source of the given size at the given
// (unclamped) distance. Returns false — no weighting needed — for
// point sources. Allocation-free; a table lookup, one lerp and a few
// flops per pair.
func spreadOrderWeights(size, dist float64, order int, ow *[spreadMaxOrder + 1]float32) bool {
	if size <= 0 {
		return false
	}
	if dist < 1e-9 {
		dist = 1e-9
	}
	// Apparent cap half-angle (the plan's spread law); saturates as the
	// listener goes inside (d → 0 ⇒ α → π, the full sphere).
	alpha := 2 * math.Atan(size/(2*dist))
	pos := alpha / math.Pi * spreadSamples
	i := int(pos)
	if i >= spreadSamples {
		i = spreadSamples - 1
	}
	t := pos - float64(i)

	var e, e0 float64
	var w [spreadMaxOrder + 1]float64
	for l := 0; l <= order; l++ {
		w[l] = spreadTable[i][l] + t*(spreadTable[i+1][l]-spreadTable[i][l])
		deg := float64(2*l + 1)
		e += deg * w[l] * w[l]
		e0 += deg
	}
	norm := math.Sqrt(e0 / e)
	if norm > spreadNormCap {
		norm = spreadNormCap
	}
	for l := 0; l <= order; l++ {
		ow[l] = float32(w[l] * norm)
	}
	return true
}

// applyOrderWeights multiplies each SH channel by its order's weight
// (order l spans channels l² .. (l+1)²−1). weights must hold at least
// (order+1)² channels.
func applyOrderWeights(order int, ow *[spreadMaxOrder + 1]float32, weights []float32) {
	for l := 0; l <= order; l++ {
		for i := l * l; i < (l+1)*(l+1); i++ {
			weights[i] *= ow[l]
		}
	}
}
