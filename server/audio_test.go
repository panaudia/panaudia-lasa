package main

import (
	"context"
	"math"
	"testing"
	"time"

	"gopkg.in/hraban/opus.v2"

	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// TestTwoClientsHearEachOther is S3's acceptance: a full audio
// round-trip through the composed server. The speaker publishes a
// 440 Hz Opus tone with poses on its mono-object track; the listener
// subscribes to its OWN binaural sink (each sink is that entity's
// rendered perspective of the space) and must find substantial energy
// in it — the speaker, 1 m away on the shared default channel.
func TestTwoClientsHearEachOther(t *testing.T) {
	a := startTestApp(t, nil)
	speaker, err := dialTest(t, a, "c-speak", "e-speak")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := dialTest(t, a, "c-listen", "e-listen")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both entities live", func() bool { return settled(a, "e-speak", "e-listen") })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	frames := make(chan []byte, 1024)
	if _, err := listener.SubscribeSink(ctx, "e-listen", "binaural", func(seq uint64, payload []byte) {
		select {
		case frames <- append([]byte(nil), payload...):
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}

	// Speak: 5 ms Opus frames of a 440 Hz sine at the real cadence, the
	// source 1 m in front of the listener's default (origin) pose.
	pub, err := speaker.Entity(ctx, "e-speak")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := opus.NewEncoder(engine.SampleRate, 1, opus.AppVoIP)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		pcm := make([]float32, wire.FrameSamples)
		buf := make([]byte, 1500)
		pose := wire.Pose{Y: 1}
		var phase float64
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for seq := uint64(0); ; seq++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			for i := range pcm {
				pcm[i] = 0.5 * float32(math.Sin(phase))
				phase += 2 * math.Pi * 440 / engine.SampleRate
			}
			n, err := enc.EncodeFloat32(pcm, buf)
			if err != nil {
				return
			}
			if pub.WriteMonoObject(seq, &wire.MonoObjectPacket{Pose: &pose, Audio: buf[:n]}) != nil {
				return
			}
		}
	}()

	// Collect renders until we find a run of energetic frames well past
	// the jitter-buffer fill (skip the first 50 ≈ 250 ms).
	dec, err := opus.NewDecoder(engine.SampleRate, 2)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, 2*wire.FrameSamples)
	var seen, energetic int
	for energetic < 20 {
		var payload []byte
		select {
		case payload = <-frames:
		case <-ctx.Done():
			t.Fatalf("timed out: %d sink frames seen, %d energetic", seen, energetic)
		}
		seen++
		if seen <= 50 {
			continue
		}
		pkt, err := wire.ParseSink(payload)
		if err != nil {
			t.Fatalf("sink frame %d: %v", seen, err)
		}
		n, err := dec.DecodeFloat32(pkt.Audio, pcm)
		if err != nil {
			t.Fatalf("opus decode of sink frame %d: %v", seen, err)
		}
		var sum float64
		for _, v := range pcm[:2*n] {
			sum += float64(v) * float64(v)
		}
		if math.Sqrt(sum/float64(2*n)) > 0.02 {
			energetic++
		}
	}
}
