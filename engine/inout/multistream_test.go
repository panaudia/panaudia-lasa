package inout

import (
	"math"
	"testing"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// Round-trip: nine distinct per-channel tones survive the multistream
// codec with their channel identities and rough levels intact.
func TestMultistreamRoundTrip(t *testing.T) {
	const channels = 9
	enc, err := NewOpusMSEncoder(channels)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.BeforeDestroy()
	dec, err := NewOpusMSDecoder(channels)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.BeforeDestroy()

	// Channel c carries a tone at 300+100c Hz at amplitude 0.1+0.05c.
	frame := make([]float32, common.FRAME_SIZE*channels)
	phases := make([]float64, channels)
	var got []float32
	// Several frames: let the codec converge past its warm-up.
	const frames = 40
	for f := 0; f < frames; f++ {
		for i := 0; i < common.FRAME_SIZE; i++ {
			for c := 0; c < channels; c++ {
				amp := 0.1 + 0.05*float64(c)
				frame[i*channels+c] = float32(amp * math.Sin(phases[c]))
				phases[c] += 2 * math.Pi * (300 + 100*float64(c)) / common.SAMPLE_RATE
			}
		}
		pkt, err := enc.Encode(frame)
		if err != nil {
			t.Fatal(err)
		}
		if len(pkt) == 0 {
			t.Fatal("empty packet")
		}
		out, err := dec.Decode(pkt)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != len(frame) {
			t.Fatalf("decoded %d samples, want %d", len(out), len(frame))
		}
		got = out // judge the last frame (post warm-up)
	}

	for c := 0; c < channels; c++ {
		var e float64
		for i := 0; i < common.FRAME_SIZE; i++ {
			v := float64(got[i*channels+c])
			e += v * v
		}
		rms := math.Sqrt(e / common.FRAME_SIZE)
		want := (0.1 + 0.05*float64(c)) / math.Sqrt2
		if rms < want*0.5 || rms > want*1.5 {
			t.Errorf("channel %d: rms %v, want ~%v — channel identity or level lost", c, rms, want)
		}
	}
}

// The encoder is allocation-free per frame after construction.
func TestMultistreamEncodeAllocs(t *testing.T) {
	if raceEnabledInout {
		t.Skip("allocation accounting differs under the race detector")
	}
	enc, err := NewOpusMSEncoder(16)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.BeforeDestroy()
	frame := make([]float32, common.FRAME_SIZE*16)
	for i := range frame {
		frame[i] = float32(math.Sin(float64(i) * 0.01))
	}
	allocs := testing.AllocsPerRun(200, func() {
		if _, err := enc.Encode(frame); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("Encode allocates %v per frame, want 0", allocs)
	}
}
