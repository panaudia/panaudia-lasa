package ambisonic

import (
	"math"
	"testing"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// The directivity law at the cardinal angles (plan: θ = 0/90/180 for
// k = 0, 0.5, 1). rel points listener→source; a source FACING the
// listener has forward = −rel/|rel|.
func TestDirectivityFactorLaw(t *testing.T) {
	rel := common.Position{X: 0, Y: 3, Z: 0} // source 3 m to the listener's left
	toward := common.Position{X: 0, Y: -1, Z: 0}
	away := common.Position{X: 0, Y: 1, Z: 0}
	side := common.Position{X: 1, Y: 0, Z: 0}

	cases := []struct {
		name    string
		forward common.Position
		k       float64
		want    float64
	}{
		{"omni toward", toward, 0, 1},
		{"omni away", away, 0, 1},
		{"half toward", toward, 0.5, 1},
		{"half side", side, 0.5, 0.5},
		{"half away", away, 0.5, 0},
		{"full toward", toward, 1, 1},
		{"full side", side, 1, 0},
		{"full away", away, 1, 0}, // clamped: (1−k)+k·cosθ = −1 → 0
		{"quarter away", away, 0.25, 0.5},
	}
	for _, c := range cases {
		if got := directivityFactor(c.forward, rel, 3, c.k); math.Abs(float64(got)-c.want) > 1e-6 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSurfaceDistance(t *testing.T) {
	if d := surfaceDistance(5, 4); d != 5 {
		t.Errorf("outside: %v", d)
	}
	if d := surfaceDistance(1, 4); d != 2 {
		t.Errorf("inside clamps to size/2: %v", d)
	}
	if d := surfaceDistance(1, 0); d != 1 {
		t.Errorf("point source: %v", d)
	}
}

func TestSpreadOrderWeights(t *testing.T) {
	var ow [spreadMaxOrder + 1]float32

	if spreadOrderWeights(0, 1, 3, &ow) {
		t.Fatal("point source must skip spread weighting")
	}

	// Far away: a 1 m source at 100 m is a point (all weights ≈ 1).
	if !spreadOrderWeights(1, 100, 3, &ow) {
		t.Fatal("sized source must weight")
	}
	for l := 0; l <= 3; l++ {
		if math.Abs(float64(ow[l])-1) > 0.01 {
			t.Errorf("far field: ow[%d] = %v, want ≈1", l, ow[l])
		}
	}

	// Deep inside: fully enveloping — higher orders vanish, the omni
	// channel takes the capped energy boost.
	spreadOrderWeights(10, 0.05, 3, &ow)
	for l := 1; l <= 3; l++ {
		if ow[l] > 0.05 {
			t.Errorf("enveloping: ow[%d] = %v, want ≈0", l, ow[l])
		}
	}
	if ow[0] < 1.0 || ow[0] > spreadNormCap+1e-6 {
		t.Errorf("enveloping: ow[0] = %v, want in [1, %v]", ow[0], spreadNormCap)
	}

	// At the surface (d = size/2): monotone decay across orders.
	spreadOrderWeights(4, 2, 3, &ow)
	for l := 1; l <= 3; l++ {
		if ow[l] >= ow[l-1] {
			t.Errorf("surface: ow[%d]=%v not < ow[%d]=%v", l, ow[l], l-1, ow[l-1])
		}
	}
}

func TestApplyOrderWeights(t *testing.T) {
	weights := make([]float32, 16)
	for i := range weights {
		weights[i] = 1
	}
	ow := [spreadMaxOrder + 1]float32{1, 0.5, 0.25, 0.125}
	applyOrderWeights(3, &ow, weights)
	wantByChannel := []float32{
		1,
		0.5, 0.5, 0.5,
		0.25, 0.25, 0.25, 0.25, 0.25,
		0.125, 0.125, 0.125, 0.125, 0.125, 0.125, 0.125,
	}
	for i, want := range wantByChannel {
		if weights[i] != want {
			t.Errorf("channel %d: %v, want %v", i, weights[i], want)
		}
	}
}
