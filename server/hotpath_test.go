package main

import (
	"math"
	"testing"
	"time"

	"gopkg.in/hraban/opus.v2"

	"github.com/panaudia/lasa/presence"
	"github.com/panaudia/lasa/server"
	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// TestIngressAllocFree drives the composed ingress hot path — raw
// datagram → Depacketizer → dof enforcement + pose fan-out → Opus
// decode → jitter write — and requires zero allocations per packet.
func TestIngressAllocFree(t *testing.T) {
	mixer, err := engine.New(engine.Config{Order: 2, MaxEntities: 4, ReverbPreset: engine.ReverbNone, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer mixer.Close()
	src, err := mixer.AddSource("e1", engine.SourceConfig{JitterWriterFrame: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	rec := &entityRec{clientID: "c1", src: src}
	rec.dof.Store(6)
	rec.slot = presence.NewCollector().Register("e1", false, sourceLoudness(src))
	rec.dep = server.NewDepacketizer(rec)

	enc, err := opus.NewEncoder(engine.SampleRate, 1, opus.AppVoIP)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, wire.FrameSamples)
	for i := range pcm {
		pcm[i] = 0.5 * float32(math.Sin(2*math.Pi*440*float64(i)/engine.SampleRate))
	}
	buf := make([]byte, 1500)
	n, err := enc.EncodeFloat32(pcm, buf)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := wire.AppendMonoObject(nil, &wire.MonoObjectPacket{
		Pose:  &wire.Pose{X: 1, Y: 2, Z: 3},
		Audio: buf[:n],
	})
	if err != nil {
		t.Fatal(err)
	}

	seq := uint64(0)
	if avg := testing.AllocsPerRun(1000, func() {
		seq++
		rec.dep.WritePacket(seq, payload)
	}); avg != 0 {
		t.Fatalf("ingress hot path allocates: %v allocs/packet", avg)
	}
}

type nullSinkWriter struct{ frames int }

func (w *nullSinkWriter) WriteFrame(frame []byte, sampleTS uint64) error {
	w.frames++
	return nil
}

// TestEgressBridgeAllocFree covers the adapter's egress addition — the
// FrameWriter→SinkWriter bridge (the shell's own writer has its own
// amortized-zero guarantee in lasa).
func TestEgressBridgeAllocFree(t *testing.T) {
	w := &nullSinkWriter{}
	sb := &sinkBridge{w: w}
	frame := make([]byte, 120)
	ts := uint64(0)
	if avg := testing.AllocsPerRun(1000, func() {
		ts += wire.FrameSamples
		sb.WriteFrame(frame, ts)
	}); avg != 0 {
		t.Fatalf("egress bridge allocates: %v allocs/frame", avg)
	}
}
