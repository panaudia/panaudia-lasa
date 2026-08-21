package main

// Head-frame entities e2e (P5): the aural HUD through the composed
// server — admitted, head-invariant, presence-flagged, source-only,
// absent from ambi fields.

import (
	"context"
	"crypto/tls"
	"sync"
	"testing"
	"time"

	"github.com/panaudia/lasa/client"
	"github.com/panaudia/lasa/connect"
	"github.com/panaudia/lasa/profile/base"
	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/inout"
)

// dialHead connects a client whose single ad-hoc entity is head-frame.
func dialHead(t *testing.T, a *app, clientID, entityID string) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, a.listener.Addr().String(), client.Config{
		SpaceID:  "main",
		ClientID: clientID,
		Entities: []connect.Entity{{ID: entityID, Name: entityID, Frame: "head"}},
		TLS:      &tls.Config{InsecureSkipVerify: true},
	})
	if err != nil {
		t.Fatalf("head-frame dial: %v", err)
	}
	return c
}

// TestHeadFrameE2E: the angel is admitted, renders at the listener's
// left ear at the same level wherever the listener goes, appears on
// presence with the head-frame flag, and is source-only.
func TestHeadFrameE2E(t *testing.T) {
	a := startTestApp(t, func(cfg *appConfig) { cfg.Reverb = "none" })

	angel := dialHead(t, a, "c-angel", "e-angel")
	defer angel.Close()
	listener, err := dialTest(t, a, "c-listen", "e-listen")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	waitFor(t, "angel admitted", func() bool { return settled(a, "e-angel", "e-listen") })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	meter := newSinkMeter(t, ctx, listener, "e-listen")
	stop := make(chan struct{})
	defer close(stop)
	// The angel speaks half a metre off the listener's LEFT ear —
	// a head-relative offset, not a world position.
	speakToneAt(t, ctx, angel, "e-angel", 2000, 0.1, wire.Pose{Y: 0.5}, stop)

	// Source-only: the angel's own sink is refused.
	if _, err := angel.SubscribeSink(ctx, "e-angel", "binaural", func(uint64, []byte) {}); err == nil {
		t.Fatal("head-frame entity's own sink must be refused (source-only)")
	}

	// Baseline level with the listener at the origin.
	for i := 0; i < 50; i++ {
		meter.next(t, ctx, "jitter fill")
	}
	var b float64
	for i := 0; i < 20; i++ {
		b += meter.next(t, ctx, "baseline")
	}
	b /= 20
	if b < 0.005 {
		t.Fatalf("angel inaudible at baseline: %v", b)
	}

	// Teleport: the listener publishes poses far away and about-faced.
	// A world source at Y:0.5 would fade to nothing at 20 m and flip
	// ears under the yaw; the angel must not move.
	pub, err := listener.Entity(ctx, "e-listen")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		pose := wire.Pose{X: 20, Y: -5, Yaw: 3.1}
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for seq := uint64(0); ; seq++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			if pub.WriteMonoObject(seq, &wire.MonoObjectPacket{Pose: &pose}) != nil {
				return
			}
		}
	}()
	meter.waitLevel(t, ctx, "angel level unchanged across teleport", 20, func(r float64) bool {
		return r > 0.8*b && r < 1.2*b
	})

	// Presence carries the head-frame flag.
	var mu sync.Mutex
	var flagged, present bool
	if _, err := listener.SubscribePresence(ctx, func(_ uint64, msg any) {
		if kf, ok := msg.(*wire.PresenceKeyframe); ok {
			mu.Lock()
			for _, r := range kf.Records {
				if r.ID == "e-angel" {
					present, flagged = true, r.HeadFrame
				}
			}
			mu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "presence keyframe flags the angel head-frame", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return present && flagged
	})
}

// TestHeadFrameAbsentFromAmbiE2E: the angel renders in binaural sinks
// but never enters a world-frame ambi field.
func TestHeadFrameAbsentFromAmbiE2E(t *testing.T) {
	a, mint := startTicketedApp(t, func(cfg *appConfig) { cfg.Reverb = "none" })

	// A presenter ticket may carry the head-frame entity ad-hoc.
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	angelClient, err := client.Dial(dialCtx, a.listener.Addr().String(), client.Config{
		SpaceID:  "main",
		ClientID: "c-angel",
		Ticket:   mint("c-angel", []string{base.RolePresenter}),
		Entities: []connect.Entity{{ID: "e-angel", Name: "e-angel", Frame: "head"}},
		TLS:      &tls.Config{InsecureSkipVerify: true},
	})
	cancelDial()
	if err != nil {
		t.Fatal(err)
	}
	defer angelClient.Close()

	listener, err := dialTicketed(t, a, "c-listen", mint("c-listen", nil, "e-listen"), "e-listen")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	producer, err := dialObserver(t, a, "c-prod", mint("c-prod", []string{base.RoleProducer}))
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	waitFor(t, "entities live", func() bool { return settled(a, "e-angel", "e-listen") })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	speakToneAt(t, ctx, angelClient, "e-angel", 2000, 0.1, wire.Pose{Y: 0.5}, stop)

	// Binaural carries the angel...
	meter := newSinkMeter(t, ctx, listener, "e-listen")
	for i := 0; i < 60; i++ {
		meter.next(t, ctx, "binaural warm-up")
	}
	meter.waitLevel(t, ctx, "angel in binaural", 10, func(r float64) bool { return r > 0.005 })

	// ...while the same listener's ambi2 field stays empty of it.
	var mu sync.Mutex
	var frames [][]byte
	if _, err := producer.SubscribeSink(ctx, "e-listen", "ambi2", func(seq uint64, payload []byte) {
		mu.Lock()
		frames = append(frames, append([]byte(nil), payload...))
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ambi frames", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(frames) > 80
	})
	mu.Lock()
	got := frames
	mu.Unlock()
	dec, err := inout.NewOpusMSDecoder(9)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.BeforeDestroy()
	var sum float64
	var n int
	for i, f := range got {
		pkt, err := wire.ParseSink(f)
		if err != nil {
			t.Fatal(err)
		}
		pcm, err := dec.Decode(pkt.Audio)
		if err != nil {
			t.Fatal(err)
		}
		if i < 40 {
			continue
		}
		for s := 0; s < len(pcm)/9; s++ {
			v := float64(pcm[s*9])
			sum += v * v
			n++
		}
	}
	if w := sum / float64(n); w > 1e-7 {
		t.Fatalf("head-frame source leaked into the ambi field (W power %v)", w)
	}
}
