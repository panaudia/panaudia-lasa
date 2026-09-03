package main

import (
	"context"
	"crypto/tls"
	"math"
	"testing"
	"time"

	"gopkg.in/hraban/opus.v2"

	"github.com/panaudia/lasa/client"
	"github.com/panaudia/lasa/connect"
	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// TestLockstepConnection runs a lockstep client through the composed
// server: two entities on one connection sharing one frame counter,
// Opus-encoded tones in step, a listener hearing the mix. It checks
// the set is built (both members report the set's size and one shared
// fill), both members' frames are delivered through the gather stage,
// the listener's sink flows, and the set is freed when the connection
// closes.
func TestLockstepConnection(t *testing.T) {
	a := startTestApp(t, func(cfg *appConfig) { cfg.Reverb = "none" })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ents := []connect.Entity{{ID: "kick", Name: "kick", Redundancy: 2}, {ID: "snare", Name: "snare"}}
	speaker, err := client.Dial(ctx, a.listener.Addr().String(), client.Config{
		SpaceID: "main", ClientID: "c-drums", Entities: ents, Lockstep: true,
		TLS: &tls.Config{InsecureSkipVerify: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := dialTest(t, a, "c-listen", "e-listen")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all live", func() bool { return settled(a, "kick", "snare", "e-listen") })
	cap := &sinkCapture{}
	if _, err := listener.SubscribeSink(ctx, "e-listen", "binaural", cap.handler); err != nil {
		t.Fatal(err)
	}

	pubs := make([]*client.EntityPublisher, len(ents))
	encs := make([]*opus.Encoder, len(ents))
	for i, e := range ents {
		if pubs[i], err = speaker.Entity(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
		if encs[i], err = opus.NewEncoder(engine.SampleRate, 1, opus.AppAudio); err != nil {
			t.Fatal(err)
		}
	}
	freqs := []float64{220, 660}
	pcm := make([]float32, wire.FrameSamples)
	buf := make([]byte, 1500)
	tick := time.NewTicker(5 * time.Millisecond)
	const frames = 300
	for seq := 0; seq < frames; seq++ {
		<-tick.C
		for i := range ents {
			for j := range pcm {
				pcm[j] = float32(0.4 * math.Sin(2*math.Pi*freqs[i]*float64(seq*wire.FrameSamples+j)/engine.SampleRate))
			}
			n, err := encs[i].EncodeFloat32(pcm, buf)
			if err != nil {
				t.Fatal(err)
			}
			pose := wire.Pose{X: float32(1 + i)}
			if err := pubs[i].WriteMonoObject(uint64(seq), &wire.MonoObjectPacket{Pose: &pose, Audio: buf[:n]}); err != nil {
				t.Fatal(err)
			}
		}
	}
	tick.Stop()

	kick, snare := a.entityStat(t, "kick"), a.entityStat(t, "snare")
	if kick.Lockstep != 2 || snare.Lockstep != 2 {
		t.Fatalf("lockstep set sizes %d/%d, want 2", kick.Lockstep, snare.Lockstep)
	}
	if kick.LatencySamples != snare.LatencySamples {
		t.Fatalf("members report different fills: %d vs %d (one shared buffer expected)", kick.LatencySamples, snare.LatencySamples)
	}
	for _, s := range []entityStats{kick, snare} {
		if s.Depacketizer.Delivered < frames-10 || s.Depacketizer.Lost > 5 || s.DecodeErrors != 0 {
			t.Fatalf("%s ingest: %+v decodeErrors %d", s.ID, s.Depacketizer, s.DecodeErrors)
		}
	}
	t.Logf("kick %+v fill %d spread %d", kick.Depacketizer, kick.LatencySamples, kick.LockstepSpread)
	if kick.LockstepSpread > 1 {
		t.Fatalf("loopback arrival spread %d frames behind the leader, want at most 1", kick.LockstepSpread)
	}
	if cap.count() < 200 {
		t.Fatalf("listener received %d sink frames, want ~300", cap.count())
	}
	mono := monoStream(t, cap.sorted())
	if len(mono) == 0 {
		t.Fatal("no sink audio")
	}
	var acc float64
	for _, v := range mono[len(mono)/2:] {
		acc += float64(v) * float64(v)
	}
	if r := math.Sqrt(acc / float64(len(mono)/2)); r < 1e-3 {
		t.Fatalf("listener heard silence (rms %g)", r)
	}

	_ = speaker.Close()
	waitFor(t, "members gone", func() bool { return settled(a, "e-listen") })
	a.backend.mu.Lock()
	groups := len(a.backend.groups)
	a.backend.mu.Unlock()
	if groups != 0 {
		t.Fatalf("%d lockstep sets still held after the connection closed", groups)
	}
}
