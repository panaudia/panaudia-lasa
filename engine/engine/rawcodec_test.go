package engine

import (
	"encoding/binary"
	"math"
	"sync"
	"testing"
)

type captureFrames struct {
	mu     sync.Mutex
	frames [][]byte
}

func (c *captureFrames) WriteFrame(b []byte, _ uint64) {
	c.mu.Lock()
	c.frames = append(c.frames, append([]byte(nil), b...))
	c.mu.Unlock()
}

func rawFrame(freq float64, frame int) []byte {
	out := make([]byte, 4*FrameSize)
	for i := 0; i < FrameSize; i++ {
		v := float32(0.3 * math.Sin(2*math.Pi*freq*float64(frame*FrameSize+i)/SampleRate))
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(v))
	}
	return out
}

// TestRawF32Codecs pins the codec-free seams the capacity harness runs
// on: a SourceCodecRawF32 source ingests float32 payloads through the
// jitter buffer, a SinkCodecRawF32 sink emits the binaurally decoded
// stereo frame as float32 bytes, a malformed raw payload is counted and
// concealed, and Conceal keeps the sample accounting moving.
func TestRawF32Codecs(t *testing.T) {
	m, err := New(Config{Order: 2, MaxEntities: 4, ReverbPreset: ReverbNone, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	src, err := m.AddSource("talker", SourceConfig{Codec: SourceCodecRawF32, InitialPose: Pose{X: 1}})
	if err != nil {
		t.Fatal(err)
	}
	cap := &captureFrames{}
	if _, err := m.AddSinkCodec("listener", cap, SinkCodecRawF32); err != nil {
		t.Fatal(err)
	}
	pose := Pose{X: 1}
	seq := uint64(0)
	for ; seq < 20; seq++ { // prime the jitter buffer
		src.WriteOpus(seq, &pose, rawFrame(440, int(seq)))
	}
	for i := 0; i < 200; i++ {
		src.WriteOpus(seq, &pose, rawFrame(440, int(seq)))
		seq++
		m.Process()
	}
	if src.DecodeErrors() != 0 {
		t.Fatalf("well-formed raw payloads counted %d decode errors", src.DecodeErrors())
	}
	cap.mu.Lock()
	frames := cap.frames
	cap.mu.Unlock()
	if len(frames) < 150 {
		t.Fatalf("sink emitted %d frames, want ~200", len(frames))
	}
	last := frames[len(frames)-1]
	if len(last) != 2*4*FrameSize {
		t.Fatalf("raw sink frame is %d bytes, want %d (stereo float32)", len(last), 2*4*FrameSize)
	}
	var acc float64
	for i := 0; i < len(last)/4; i++ {
		v := float64(math.Float32frombits(binary.LittleEndian.Uint32(last[4*i:])))
		acc += v * v
	}
	if rms := math.Sqrt(acc / float64(len(last)/4)); rms < 1e-3 {
		t.Fatalf("raw sink frame is silent (rms %g): audio did not flow through the codec-free path", rms)
	}

	// A payload that is not whole samples is a decode error, concealed.
	src.WriteOpus(seq, &pose, []byte{1, 2, 3})
	seq++
	if src.DecodeErrors() != 1 {
		t.Fatalf("malformed raw payload: %d decode errors, want 1", src.DecodeErrors())
	}
	// Conceal advances the accounting a full frame without a decoder.
	before := src.samplesWritten
	src.Conceal(seq, FrameSize)
	if src.samplesWritten != before+FrameSize {
		t.Fatalf("Conceal advanced %d samples, want %d", src.samplesWritten-before, FrameSize)
	}
}
