package main

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/panaudia/lasa/profile/base"
	"github.com/panaudia/lasa/wire"
)

// TestPresenceFeed is S5's presence acceptance: the live feed through
// the composed server carries the roster with the published (enforced)
// poses and the ENGINE's post-gain loudness — the composition lasa's own
// e2e cannot exercise (its stub feeds synthetic loudness).
func TestPresenceFeed(t *testing.T) {
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
	stop := make(chan struct{})
	defer close(stop)
	speakTone(t, ctx, speaker, "e-speak", 0.1, stop) // pose Y=1, ~-23 dBFS source

	var mu sync.Mutex
	latest := map[string]wire.KeyframeRecord{}
	if _, err := listener.SubscribePresence(ctx, func(_ uint64, msg any) {
		if kf, ok := msg.(*wire.PresenceKeyframe); ok {
			mu.Lock()
			for _, r := range kf.Records {
				latest[r.ID] = r
			}
			mu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "keyframe with both entities, speaker pose+loudness, listener silent", func() bool {
		mu.Lock()
		defer mu.Unlock()
		spk, ok1 := latest["e-speak"]
		lst, ok2 := latest["e-listen"]
		if !ok1 || !ok2 {
			return false
		}
		// The speaker's record reflects its published pose and audible
		// post-gain loudness (0.1-amp sine ≈ -23 dBFS).
		db := spk.Loudness.DBFS()
		if math.Abs(float64(spk.Pose.Y)-1) > 0.01 || db < -45 || db > -8 {
			return false
		}
		// The listener publishes nothing: home pose, silent source.
		return lst.Pose == (wire.Pose{}) && lst.Loudness == wire.LoudnessSilent
	})
}

// TestBlockKickE2E is S5's moderation acceptance: a moderator's state
// write through the gate kicks the live client via the normal departure
// funnel, gates its reconnect with the §4.6 blocked code, and clearing
// the key restores admission (a kick is a block with an expiry; forever
// here, cleared explicitly).
func TestBlockKickE2E(t *testing.T) {
	a, mint := startTicketedApp(t)
	if _, err := dialTicketed(t, a, "c-target", mint("c-target", nil, "e-target"), "e-target"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "target live", func() bool { return settled(a, "e-target") })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mod, err := dialObserver(t, a, "c-mod", mint("c-mod", []string{base.RoleModerator}))
	if err != nil {
		t.Fatal(err)
	}
	defer mod.Close()

	// Kick: block forever. The live session is severed and departs
	// through the funnel — both invariant halves drain.
	if err := mod.WriteState(ctx, wire.Set{Key: "lasa.space.client.blocked.c-target", Value: []byte("0")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "target severed through the funnel", func() bool { return settled(a) })

	// A blocked reconnect is refused at the gate with the §4.6 code.
	_, err = dialTicketed(t, a, "c-target", mint("c-target", nil, "e-target"), "e-target")
	if err == nil {
		t.Fatal("blocked client must not reconnect")
	}
	var appErr *quic.ApplicationError
	if !errors.As(err, &appErr) || appErr.ErrorCode != quic.ApplicationErrorCode(wire.ErrCodeBlocked) {
		t.Fatalf("err = %v, want application error %#x", err, wire.ErrCodeBlocked)
	}

	// Clearing the block restores admission.
	if err := mod.WriteState(ctx, wire.Clear{Key: "lasa.space.client.blocked.c-target"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = dialTicketed(t, a, "c-target", mint("c-target", nil, "e-target"), "e-target"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unblocked client cannot reconnect: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, "target readmitted", func() bool { return settled(a, "e-target") })
}
