package main

// P4 acceptance: the ambisonic sink formats through the composed
// server — order-derived offering (no role gate, lasa-core.md §3),
// decodable SN3D multistream, simultaneous with the entity's own
// binaural render.

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/panaudia/lasa/profile/base"
	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/inout"
)

func TestAmbiE2E(t *testing.T) {
	a, mint := startTicketedApp(t, func(cfg *appConfig) { cfg.Reverb = "none" })

	speaker, err := dialTicketed(t, a, "c-speak", mint("c-speak", nil, "e-speak"), "e-speak")
	if err != nil {
		t.Fatal(err)
	}
	defer speaker.Close()
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
	waitFor(t, "entities live", func() bool { return settled(a, "e-speak", "e-listen") })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The entity's own binaural keeps playing throughout.
	meter := newSinkMeter(t, ctx, listener, "e-listen")
	stop := make(chan struct{})
	defer close(stop)
	speakToneAt(t, ctx, speaker, "e-speak", 440, 0.1, wire.Pose{X: 1}, stop)

	// Formats carry no role gate: the audience listener may pull its
	// own sink in any offered format, ambi included.
	if _, err := listener.SubscribeSink(ctx, "e-listen", "ambi3", func(uint64, []byte) {}); err != nil {
		t.Fatalf("owner must not be refused its own ambi3: %v", err)
	}

	// The producer taps e-listen's ambi3 field (e-listen hears e-speak;
	// its own source is self-excluded, as in its binaural perspective).
	var mu sync.Mutex
	var frames [][]byte
	if _, err := producer.SubscribeSink(ctx, "e-listen", "ambi3", func(seq uint64, payload []byte) {
		mu.Lock()
		frames = append(frames, append([]byte(nil), payload...))
		mu.Unlock()
	}); err != nil {
		t.Fatalf("producer ambi3 subscribe: %v", err)
	}

	waitFor(t, "ambi frames flowing", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(frames) >= 100
	})

	// Field sanity: decode the multistream, skip warm-up, and check the
	// scene — energy in W, dead-ahead source ⇒ X ≈ W (SN3D) and Y ≈ 0.
	mu.Lock()
	got := frames
	mu.Unlock()
	dec, err := inout.NewOpusMSDecoder(16)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.BeforeDestroy()
	sums := make([]float64, 16)
	var n int
	for i, f := range got {
		pkt, err := wire.ParseSink(f)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		pcm, err := dec.Decode(pkt.Audio)
		if err != nil {
			t.Fatalf("frame %d decode: %v", i, err)
		}
		if i < 50 {
			continue // jitter fill + codec warm-up
		}
		for s := 0; s < len(pcm)/16; s++ {
			for c := 0; c < 16; c++ {
				v := float64(pcm[s*16+c])
				sums[c] += v * v
			}
		}
		n += len(pcm) / 16
	}
	rms := func(c int) float64 { return math.Sqrt(sums[c] / float64(n)) }
	if rms(0) < 0.005 {
		t.Fatalf("W channel too quiet: %v", rms(0))
	}
	if r := rms(3) / rms(0); r < 0.7 || r > 1.3 {
		t.Errorf("X/W = %v, want ≈1 (SN3D, dead-ahead source)", r)
	}
	if r := rms(1) / rms(0); r > 0.3 {
		t.Errorf("Y/W = %v, want ≈0", r)
	}

	// And the entity's own binaural stayed alive beside the tap.
	meter.waitLevel(t, ctx, "binaural still flowing beside ambi", 10, func(r float64) bool { return r > 0.005 })
}

// TestAmbiOfferingByOrder: an order-2 space offers ambi2 but not ambi3.
func TestAmbiOfferingByOrder(t *testing.T) {
	a, mint := startTicketedApp(t, func(cfg *appConfig) {
		cfg.Order = 2
		cfg.Reverb = "none"
	})
	target, err := dialTicketed(t, a, "c-t", mint("c-t", nil, "e-t"), "e-t")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	producer, err := dialObserver(t, a, "c-p", mint("c-p", []string{base.RoleProducer}))
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	waitFor(t, "entity live", func() bool { return settled(a, "e-t") })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := producer.SubscribeSink(ctx, "e-t", "ambi3", func(uint64, []byte) {}); err == nil {
		t.Fatal("ambi3 must not be offered by an order-2 space")
	}
	var got atomic32
	if _, err := producer.SubscribeSink(ctx, "e-t", "ambi2", func(uint64, []byte) { got.inc() }); err != nil {
		t.Fatalf("ambi2 on an order-2 space: %v", err)
	}
	waitFor(t, "ambi2 frames", func() bool { return got.load() > 20 })
}

type atomic32 struct {
	mu sync.Mutex
	n  int
}

func (a *atomic32) inc() { a.mu.Lock(); a.n++; a.mu.Unlock() }
func (a *atomic32) load() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}
