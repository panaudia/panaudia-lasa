package engine

import (
	"math"
	"testing"

	opus "gopkg.in/hraban/opus.v2"
)

// A packet libopus rejects must never panic the engine (the bytes came
// from a client): it is concealed like a lost packet, so the stream
// accounting stays exact, and counted.
func TestDecodeErrorIsConcealedAndCounted(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	src, err := m.AddSource("s", SourceConfig{InitialPose: Pose{X: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("l", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}

	enc, err := opus.NewEncoder(SampleRate, 1, opus.AppVoIP)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, FrameSize)
	pkt := make([]byte, 4000)
	encodeFrame := func(f int) []byte {
		for i := range pcm {
			pcm[i] = float32(0.4 * math.Sin(float64(f*FrameSize+i)*0.05))
		}
		n, err := enc.EncodeFloat32(pcm, pkt)
		if err != nil {
			t.Fatal(err)
		}
		return pkt[:n]
	}
	// TOC code 3 (arbitrary frame count) with the count byte missing:
	// OPUS_INVALID_PACKET, deterministically.
	garbage := []byte{0xff}

	const total = 20
	bad := map[int]bool{5: true, 12: true, 13: true}
	for f := 0; f < total; f++ {
		p := Pose{X: 1 + float64(f)*0.1}
		if bad[f] {
			src.WriteOpus(uint64(f+1), &p, garbage)
		} else {
			src.WriteOpus(uint64(f+1), &p, encodeFrame(f))
		}
		m.Process()
	}

	if got, want := src.DecodeErrors(), uint64(len(bad)); got != want {
		t.Fatalf("DecodeErrors = %d, want %d", got, want)
	}
	// Every packet, rejected ones included, advanced the stream by one
	// frame — the pose ring's alignment depends on it.
	if got, want := src.samplesWritten, uint64(total*FrameSize); got != want {
		t.Fatalf("stream accounting: %d samples written, want %d", got, want)
	}
	// And a good packet after the bad ones still decodes.
	src.WriteOpus(total+1, nil, encodeFrame(total))
	if got := src.DecodeErrors(); got != uint64(len(bad)) {
		t.Fatalf("a valid packet after garbage was counted as an error (%d)", got)
	}
}
