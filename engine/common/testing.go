package common

import (
	"fmt"
	"math"
	"testing"
)

func AssertArraysEqual(t *testing.T, A []float32, B []float32) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if v != B[i] {
			t.Fatalf(`arrays not equal - got: %v expected: %v`, A, B)
		}
	}
}

func AssertApproxArraysEqual(t *testing.T, A []float32, B []float32) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		// Relative-aware tolerance: compared paths compute the same
		// quantity via different float32 formula orderings (Go math.Pow on
		// the meters-native path vs the SAF polar-form reference), so the
		// error scales with magnitude at ~1e-5 relative. The old fixed
		// 1e-5 absolute gate only passed on large near-source gains while
		// the clamp pinned both paths to exactly 1.0 (unpegged to
		// NearFieldGainCap after the M4–M6 listening pass).
		tol := 0.00003 * math.Max(1.0, math.Abs(float64(B[i])))
		if math.Abs(float64(v-B[i])) > tol {
			fmt.Printf(`%d %v %v\n`, i, v, B[i])
			t.Fatalf(`arrays not equal - got: %v expected: %v`, A, B)
		}
	}
}

func AssertApproxishArraysEqual(t *testing.T, A []float32, B []float32) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if math.Abs(float64(v-B[i])) > 0.01 {
			t.Fatalf(`arrays not equal - got: %v expected: %v`, A, B)
		}
	}
}

func AssertArraysEqualInt(t *testing.T, A []int, B []int) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if v != B[i] {
			t.Fatalf(`arrays not equal - got: %v expected: %v`, A, B)
		}
	}
}

// XCorrArgmax is the shared cross-correlation lag estimator for the
// ITD/latency test suites (m4 bus ITD, convolver anchor alignment, ear
// lag): it returns the lag in [loLag, hiLag] maximizing
// Σ_{t=t0}^{t1-1} a[t]·b[t+lag], accumulated in float64, plus the per-lag
// scores for parabolic refinement. One estimator across suites so an ITD
// regression cannot pass one suite and fail another for estimator reasons.
// Callers must pick t0/t1/lag bounds so b[t+lag] stays in range.
func XCorrArgmax[F ~float32 | ~float64](a, b []F, loLag, hiLag, t0, t1 int) (bestLag int, scores map[int]float64) {
	best := math.Inf(-1)
	bestLag = loLag
	scores = make(map[int]float64, hiLag-loLag+1)
	for lag := loLag; lag <= hiLag; lag++ {
		var acc float64
		for t := t0; t < t1; t++ {
			acc += float64(a[t]) * float64(b[t+lag])
		}
		scores[lag] = acc
		if acc > best {
			best, bestLag = acc, lag
		}
	}
	return bestLag, scores
}

func AssertArraysAlmostEqual(t *testing.T, A []float32, B []float32) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if math.Abs(float64(v-B[i])) > 0.000001 {
			fmt.Printf(`%v %v`, v, B[i])
			t.Fatalf(`arrays not almost equal - got: %v expected: %v`, A, B)
		}
	}
}
