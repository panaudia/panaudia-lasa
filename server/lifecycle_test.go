package main

import (
	"context"
	"crypto/tls"
	"sort"
	"testing"
	"time"

	"github.com/panaudia/lasa/client"
	"github.com/panaudia/lasa/connect"
)

func startTestApp(t *testing.T, mod func(*appConfig)) *app {
	t.Helper()
	cfg := defaultConfig()
	cfg.Host, cfg.Port = "localhost", 0
	if mod != nil {
		mod(&cfg)
	}
	a, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.serve(context.Background()) }()
	t.Cleanup(a.close)
	return a
}

func dialTest(t *testing.T, a *app, clientID string, entityIDs ...string) (*client.Client, error) {
	t.Helper()
	ents := make([]connect.Entity, len(entityIDs))
	for i, id := range entityIDs {
		ents[i] = connect.Entity{ID: id, Name: id}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Dial(ctx, a.listener.Addr().String(), client.Config{
		SpaceID:  "main",
		ClientID: clientID,
		Entities: ents,
		TLS:      &tls.Config{InsecureSkipVerify: true},
	})
}

func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", desc)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settled reports whether both halves of the §5 invariant hold the
// wanted id set: conn-map ≡ engine entity set ≡ want.
func settled(a *app, want ...string) bool {
	sort.Strings(want)
	bs, es := a.backend.entitySnapshot(), a.mixer.Entities()
	sort.Strings(bs)
	sort.Strings(es)
	if len(bs) != len(want) || len(es) != len(want) {
		return false
	}
	for i := range want {
		if bs[i] != want[i] || es[i] != want[i] {
			return false
		}
	}
	return true
}

func ownerOf(a *app, entityID string) string {
	a.backend.mu.Lock()
	defer a.backend.mu.Unlock()
	if rec := a.backend.entities[entityID]; rec != nil {
		return rec.clientID
	}
	return ""
}

func TestLifecycleConnectDisconnect(t *testing.T) {
	a := startTestApp(t, nil)
	c, err := dialTest(t, a, "c1", "e1", "e2")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "join of e1+e2", func() bool { return settled(a, "e1", "e2") })
	_ = c.Close()
	waitFor(t, "departure sweep", func() bool { return settled(a) })
}

func TestEvictOldEntityCollision(t *testing.T) {
	a := startTestApp(t, nil)
	if _, err := dialTest(t, a, "c1", "e1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "c1's e1", func() bool { return settled(a, "e1") && ownerOf(a, "e1") == "c1" })

	// Same entity id from a different client, unticketed space:
	// newest-wins is total in dev mode (id-capabilities.md; the
	// ticketed space refuses instead — TestTicketedEntityCollisionRefused).
	// The engine adjudicates sweep-before-apply atomically and the
	// shell orders the predecessor's teardown before EntityJoined, so
	// the handoff is atomic from the conn-map's view.
	if _, err := dialTest(t, a, "c2", "e1"); err != nil {
		t.Fatalf("replacement admission: %v", err)
	}
	waitFor(t, "handoff to c2", func() bool { return settled(a, "e1") && ownerOf(a, "e1") == "c2" })
}

func TestEvictOldSameClientReconnect(t *testing.T) {
	a := startTestApp(t, nil)
	if _, err := dialTest(t, a, "c1", "e1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first session", func() bool { return settled(a, "e1") })

	// Same client id, disjoint entity set: the reconnect displaces the
	// whole predecessor session, not just colliding entities.
	if _, err := dialTest(t, a, "c1", "e2"); err != nil {
		t.Fatalf("reconnect admission: %v", err)
	}
	waitFor(t, "reconnect settled", func() bool { return settled(a, "e2") })
}

func TestAdmitAtCapacity(t *testing.T) {
	a := startTestApp(t, func(cfg *appConfig) { cfg.MaxEntities = 2 })
	if _, err := dialTest(t, a, "c1", "e1", "e2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "space full", func() bool { return settled(a, "e1", "e2") })

	if _, err := dialTest(t, a, "c2", "e3"); err == nil {
		t.Fatal("admission beyond capacity succeeded")
	}
	waitFor(t, "unchanged after refusal", func() bool { return settled(a, "e1", "e2") })
}
