package inout

import "testing"

// A malformed packet is an error, never a panic, and the decoder stays
// usable afterwards.
func TestOpusInputDecoderRejectsMalformed(t *testing.T) {
	for _, channels := range []int{1, 2} {
		d := NewOpusInputDecoder(channels)
		if _, err := d.Decode([]byte{0xff}); err == nil {
			t.Fatalf("channels=%d: malformed packet decoded without error", channels)
		}
		// Concealment still works on the same decoder.
		if got := len(d.ConcealFloat32(240)); got != 240 {
			t.Fatalf("channels=%d: conceal after error returned %d samples", channels, got)
		}
	}
}
