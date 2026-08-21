package ambisonic

import "math"

// ConvertN3DtoSN3DInPlace rescales a channel-major ambisonic frame
// ([channel][size] float32, ACN order) from N3D to SN3D normalisation:
// every degree-n channel (ACN n²..(n+1)²−1) is scaled by 1/√(2n+1).
//
// Bit-exact pure-Go port of SAF's convertHOANormConvention(N3D→SN3D)
// (M9.2, plan/m9-saf-exit/plan.md): float32(math.Sqrt(float64(x))) equals
// C's sqrtf(x) for these inputs (single-rounding-safe for sqrt), and the
// scale is applied with the same float32 divide/multiply as cblas_sscal.
func ConvertN3DtoSN3DInPlace(src []float32, order int, size int) {
	for n := 1; n <= order; n++ {
		scale := float32(1.0) / float32(math.Sqrt(float64(2*n+1)))
		for ch := n * n; ch < (n+1)*(n+1); ch++ {
			seg := src[ch*size : (ch+1)*size]
			for i := range seg {
				seg[i] *= scale
			}
		}
	}
}
