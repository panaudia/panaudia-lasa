package buffers

import (
	"slices"
	"testing"
)

func TestNewCBuffer(t *testing.T) {
	b := NewCBuffer(4)

	if b.p_data == 0 {
		t.Fatalf("NewCBuffer(4).p_data = %#x, want non-zero", b.p_data)
	}
	if b.count != 4 {
		t.Fatalf("NewCBuffer(4).count = %d, want 4", b.count)
	}

	// A freshly allocated buffer is zeroed.
	got := make([]float32, 4)
	b.CopyToSlice(got)
	if want := []float32{0, 0, 0, 0}; !slices.Equal(got, want) {
		t.Fatalf("new buffer = %v, want %v", got, want)
	}
}

func TestCopyFromSlice(t *testing.T) {
	b := NewCBuffer(4)
	src := []float32{1.5, 4, 3, 7.5}
	got := make([]float32, 4)

	b.CopyFromSlice(src)
	b.CopyToSlice(got)

	if !slices.Equal(got, src) {
		t.Fatalf("CopyFromSlice round-trip = %v, want %v", got, src)
	}
}

func TestCopyFromCBuffer(t *testing.T) {
	src := []float32{1.5, 4, 3, 7.5}
	b := NewCBuffer(4)
	c := NewCBuffer(4)
	got := make([]float32, 4)

	b.CopyFromSlice(src)
	c.CopyFromCBuffer(b)
	c.CopyToSlice(got)

	if !slices.Equal(got, src) {
		t.Fatalf("CopyFromCBuffer = %v, want %v", got, src)
	}
}

func TestClearBuffer(t *testing.T) {
	b := NewCBuffer(4)
	got := make([]float32, 4)

	b.CopyFromSlice([]float32{1.5, 4, 3, 7.5})
	b.Clear()
	b.CopyToSlice(got)

	if want := []float32{0, 0, 0, 0}; !slices.Equal(got, want) {
		t.Fatalf("after Clear = %v, want %v", got, want)
	}
}

func TestInterleaveBuffer(t *testing.T) {
	a := NewCBuffer(4)
	b := NewCBuffer(4)
	c := NewCBuffer(8)
	got := make([]float32, 8)

	a.CopyFromSlice([]float32{1.0, 2.0, 3.0, 4.0})
	b.CopyFromSlice([]float32{1.5, 2.5, 3.5, 4.5})

	c.InterleaveCBuffers(a, b)
	c.CopyToSlice(got)

	want := []float32{1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5}
	if !slices.Equal(got, want) {
		t.Fatalf("InterleaveCBuffers = %v, want %v", got, want)
	}
}
