package ambisonic

import (
	"math"
	"testing"
)

// TestN3DtoSN3DFactors checks the conversion against the analytic factors:
// channel of degree n scaled by 1/sqrt(2n+1), W untouched. Permanent.
func TestN3DtoSN3DFactors(t *testing.T) {
	const order, size = 3, 8
	nCh := (order + 1) * (order + 1)
	data := make([]float32, nCh*size)
	for i := range data {
		data[i] = 1.0
	}
	ConvertN3DtoSN3DInPlace(data, order, size)
	for ch := 0; ch < nCh; ch++ {
		n := int(math.Floor(math.Sqrt(float64(ch))))
		want := 1.0 / math.Sqrt(float64(2*n+1))
		for s := 0; s < size; s++ {
			got := float64(data[ch*size+s])
			if math.Abs(got-want) > 1e-7 {
				t.Fatalf("ch %d (degree %d) sample %d: got %g want %g",
					ch, n, s, got, want)
			}
		}
	}
}
