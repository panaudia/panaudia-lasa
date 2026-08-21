package main

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/panaudia/lasa/client"
	"github.com/panaudia/lasa/connect"
	"github.com/panaudia/lasa/state/clientstore"
)

// TestBootTickConnect is S1's acceptance: the composed server starts,
// the render tick advances (the one clock is running), and a real lasa
// client completes the handshake against the skeleton backend.
func TestBootTickConnect(t *testing.T) {
	cfg := defaultConfig()
	cfg.Host, cfg.Port = "localhost", 0 // ephemeral, loopback
	a, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	go func() { _ = a.serve() }()

	deadline := time.Now().Add(2 * time.Second)
	for a.mixer.Stats().Ticks == 0 {
		if time.Now().After(deadline) {
			t.Fatal("mixer never ticked")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := clientstore.New(cfg.SpaceID, []string{""}, clientstore.NewMemPersistence())
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Dial(ctx, a.listener.Addr().String(), client.Config{
		SpaceID:  cfg.SpaceID,
		ClientID: "smoke",
		Entities: []connect.Entity{{ID: "probe", Name: "Probe"}},
		TLS:      &tls.Config{InsecureSkipVerify: true},
		Store:    store,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// State round-trip: the admission write (the probe's marker key)
	// reaches the client's synced store.
	go func() { _ = c.SyncState(ctx) }()
	for {
		var ok bool
		c.View(func(s *clientstore.Store) { _, ok = s.Get("lasa.entity.probe") })
		if ok {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("entity marker never reached the client store")
		}
		time.Sleep(10 * time.Millisecond)
	}

	before := a.mixer.Stats().Ticks
	time.Sleep(50 * time.Millisecond)
	if after := a.mixer.Stats().Ticks; after <= before {
		t.Fatalf("tick stalled while serving: %d -> %d", before, after)
	}
}
