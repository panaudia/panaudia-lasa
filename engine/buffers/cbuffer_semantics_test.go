package buffers

import (
	"math/rand"
	"testing"
)

func randFloats(rng *rand.Rand, n int) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = rng.Float32()*2 - 1
	}
	return s
}

// TestSemantics covers the CBuffer contract independent of the C
// reference (permanent).
func TestSemantics(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	const n = 240

	b := NewCBuffer(n)
	defer b.BeforeDestroy()
	for i, v := range b.AsUnsafeFloatSlice() {
		if v != 0 {
			t.Fatalf("new buffer not zeroed at %d", i)
		}
	}

	// CopyFromSlice bounds by source length
	short := randFloats(rng, 10)
	b.CopyFromSlice(short)
	s := b.AsUnsafeFloatSlice()
	for i := 0; i < 10; i++ {
		if s[i] != short[i] {
			t.Fatalf("copy sample %d mismatch", i)
		}
	}
	if s[10] != 0 {
		t.Fatal("copy overran source length")
	}

	// child buffer aliases the parent
	child := NewChildCBuffer(20, b, 5)
	defer child.BeforeDestroy()
	child.AsUnsafeFloatSlice()[0] = 42
	if s[5] != 42 {
		t.Fatal("child buffer does not alias parent")
	}

	// Clear zeroes
	b.Clear()
	for i, v := range s {
		if v != 0 {
			t.Fatalf("clear left %g at %d", v, i)
		}
	}
}
