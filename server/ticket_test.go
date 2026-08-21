package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/panaudia/lasa/client"
	"github.com/panaudia/lasa/connect"
	"github.com/panaudia/lasa/profile/base"
	"github.com/panaudia/lasa/state/clientstore"
	"github.com/panaudia/lasa/wire"
)

// startTicketedApp boots a ticketed-only space (AllowUnticketed=false —
// the S3.5 acceptance posture) and returns a mint function for its one
// issuer key. The server itself only ever sees the public half.
func startTicketedApp(t *testing.T, mods ...func(*appConfig)) (*app, func(clientID string, roles []string, entityIDs ...string) string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := startTestApp(t, func(cfg *appConfig) {
		cfg.TicketKey = pub
		cfg.AllowUnticketed = false
		for _, mod := range mods {
			mod(cfg)
		}
	})
	mint := func(clientID string, roles []string, entityIDs ...string) string {
		var ents []connect.Entity
		for _, id := range entityIDs {
			ents = append(ents, connect.Entity{ID: id, Name: id})
		}
		tk, err := connect.Mint(priv, connect.MintClaims{
			Issuer:   "test-issuer",
			SpaceID:  "main",
			ClientID: clientID,
			Roles:    roles,
			Entities: ents,
			IssuedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	return a, mint
}

// dialTicketed connects with a ticket, instantiating the ticket-defined
// entities via setups (the signed path — ad-hoc needs admin/presenter).
func dialTicketed(t *testing.T, a *app, clientID, ticket string, entityIDs ...string) (*client.Client, error) {
	t.Helper()
	setups := make([]connect.Setup, len(entityIDs))
	for i, id := range entityIDs {
		setups[i] = connect.Setup{ID: id}
	}
	store, err := clientstore.New("main", []string{""}, clientstore.NewMemPersistence())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Dial(ctx, a.listener.Addr().String(), client.Config{
		SpaceID:  "main",
		ClientID: clientID,
		Ticket:   ticket,
		Setups:   setups,
		TLS:      &tls.Config{InsecureSkipVerify: true},
		Store:    store,
	})
}

// dialObserver connects a zero-entity console (the observer pattern,
// core §4.2) with the given ticket.
func dialObserver(t *testing.T, a *app, clientID, ticket string) (*client.Client, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Dial(ctx, a.listener.Addr().String(), client.Config{
		SpaceID:  "main",
		ClientID: clientID,
		Ticket:   ticket,
		Entities: []connect.Entity{},
		TLS:      &tls.Config{InsecureSkipVerify: true},
	})
}

// viewHas reports whether the client's synced store holds the key.
func viewHas(c *client.Client, key string) bool {
	var ok bool
	c.View(func(s *clientstore.Store) { _, ok = s.Get(key) })
	return ok
}

// TestTicketedAdmission: unticketed connects are refused outright; a
// ticketed connect instantiates its signed entities.
func TestTicketedAdmission(t *testing.T) {
	a, mint := startTicketedApp(t)

	if _, err := dialTest(t, a, "c-open", "e-open"); err == nil {
		t.Fatal("unticketed dial must fail in a ticketed-only space")
	}

	c, err := dialTicketed(t, a, "c1", mint("c1", nil, "e1"), "e1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitFor(t, "signed entity admitted", func() bool { return settled(a, "e1") })
}

// TestAdHocByRole: ad-hoc entity definitions in the config are a
// presenter/admin privilege; an audience ticket is refused.
func TestAdHocByRole(t *testing.T) {
	a, mint := startTicketedApp(t)

	dialAdHoc := func(clientID string, roles []string, entityID string) (*client.Client, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return client.Dial(ctx, a.listener.Addr().String(), client.Config{
			SpaceID:  "main",
			ClientID: clientID,
			Ticket:   mint(clientID, roles),
			Entities: []connect.Entity{{ID: entityID, Name: entityID}},
			TLS:      &tls.Config{InsecureSkipVerify: true},
		})
	}

	if _, err := dialAdHoc("c-aud", nil, "e-aud"); err == nil {
		t.Fatal("audience ticket must not carry ad-hoc entities")
	}
	c, err := dialAdHoc("c-pres", []string{base.RolePresenter}, "e-pres")
	if err != nil {
		t.Fatalf("presenter ad-hoc admission: %v", err)
	}
	defer c.Close()
	waitFor(t, "ad-hoc entity admitted", func() bool { return settled(a, "e-pres") })
}

// TestRoleDerivedWrites: an audience write to lasa.space.* is silently
// filtered by the role-derived rules; a producer's identical write lands.
func TestRoleDerivedWrites(t *testing.T) {
	a, mint := startTicketedApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	aud, err := dialTicketed(t, a, "c-aud", mint("c-aud", nil, "e-aud"), "e-aud")
	if err != nil {
		t.Fatal(err)
	}
	defer aud.Close()
	prod, err := dialTicketed(t, a, "c-prod", mint("c-prod", []string{base.RoleProducer}, "e-prod"), "e-prod")
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	go func() { _ = aud.SyncState(ctx) }()

	// The audience client tries a producer key, then writes something it
	// IS allowed. Both ride the same connection in order, so once the
	// allowed write is visible the refused one has had every chance.
	if err := aud.WriteState(ctx, wire.Set{Key: "lasa.space.channel.gain.forbidden", Value: []byte("2.0")}); err != nil {
		t.Fatal(err)
	}
	if err := aud.WriteState(ctx, wire.Set{Key: "lasa.entity.e-aud.attrs.marker", Value: []byte("true")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "audience's own attr write", func() bool {
		return viewHas(aud, "lasa.entity.e-aud.attrs.marker")
	})
	if viewHas(aud, "lasa.space.channel.gain.forbidden") {
		t.Fatal("audience write to lasa.space.* must be refused")
	}

	if err := prod.WriteState(ctx, wire.Set{Key: "lasa.space.channel.gain.main", Value: []byte("1.5")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "producer's channel gain write", func() bool {
		return viewHas(aud, "lasa.space.channel.gain.main")
	})
}

// TestSinkAllByRole: sink subscription beyond the owner is the
// admin/producer grant — the first cross-connection sink subscriber.
func TestSinkAllByRole(t *testing.T) {
	a, mint := startTicketedApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	aud, err := dialTicketed(t, a, "c-aud", mint("c-aud", nil, "e-aud"), "e-aud")
	if err != nil {
		t.Fatal(err)
	}
	defer aud.Close()
	prod, err := dialTicketed(t, a, "c-prod", mint("c-prod", []string{base.RoleProducer}, "e-prod"), "e-prod")
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	waitFor(t, "both entities admitted", func() bool { return settled(a, "e-aud", "e-prod") })

	if _, err := aud.SubscribeSink(ctx, "e-prod", "binaural", func(uint64, []byte) {}); err == nil {
		t.Fatal("audience must not subscribe to another entity's sink")
	}
	if _, err := prod.SubscribeSink(ctx, "e-aud", "binaural", func(uint64, []byte) {}); err != nil {
		t.Fatalf("producer listening through e-aud's ears: %v", err)
	}
}

// TestTicketedSameClientSupersedes: newest exercise wins (core §4.4) —
// a reconnect bearing the same signed client_id displaces the whole
// predecessor session, immediately, without waiting on death detection.
func TestTicketedSameClientSupersedes(t *testing.T) {
	a, mint := startTicketedApp(t)

	first, err := dialTicketed(t, a, "c1", mint("c1", nil, "e1", "e2"), "e1")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first exercise", func() bool { return settled(a, "e1") })

	second, err := dialTicketed(t, a, "c1", mint("c1", nil, "e1", "e2"), "e2")
	if err != nil {
		t.Fatalf("re-exercise must supersede, not fail: %v", err)
	}
	defer second.Close()
	waitFor(t, "supersession handoff", func() bool { return settled(a, "e2") })
	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("superseded predecessor still alive")
	}
}

// TestTicketedEntityCollisionRefused: an entity id live under a
// DIFFERENT client refuses the newcomer (core §4.4, 0x1A5A04) — a
// bystander is never severed on entity grounds, and the refused
// admission leaves the incumbent untouched.
func TestTicketedEntityCollisionRefused(t *testing.T) {
	a, mint := startTicketedApp(t)

	c1, err := dialTicketed(t, a, "c1", mint("c1", nil, "e1"), "e1")
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	waitFor(t, "incumbent", func() bool { return settled(a, "e1") && ownerOf(a, "e1") == "c1" })

	if _, err := dialTicketed(t, a, "c2", mint("c2", nil, "e1"), "e1"); err == nil {
		t.Fatal("cross-client entity collision must refuse")
	}
	if !settled(a, "e1") || ownerOf(a, "e1") != "c1" {
		t.Fatal("refused admission must leave the incumbent untouched")
	}
	select {
	case <-c1.Done():
		t.Fatal("incumbent severed by a refused admission")
	default:
	}
}

// TestObserverSupersession: zero-entity sessions collide on the client
// claim alone — the previously invisible case (id-capabilities.md).
func TestObserverSupersession(t *testing.T) {
	a, mint := startTicketedApp(t)

	first, err := dialObserver(t, a, "c1", mint("c1", nil))
	if err != nil {
		t.Fatal(err)
	}
	second, err := dialObserver(t, a, "c1", mint("c1", nil))
	if err != nil {
		t.Fatalf("observer re-exercise must supersede, not fail: %v", err)
	}
	defer second.Close()
	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("superseded observer still alive")
	}
}

// TestModeMismatchTicketToOpenSpace: a space is ticketed XOR unticketed
// (core §4.4) — an unticketed space rejects any presented ticket
// (0x1A5A08), valid or not; mode is checked before verification.
func TestModeMismatchTicketToOpenSpace(t *testing.T) {
	a := startTestApp(t, nil) // open space (in-process default)
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := connect.Mint(priv, connect.MintClaims{
		Issuer: "test-issuer", SpaceID: "main", ClientID: "c1",
		Entities: []connect.Entity{{ID: "e1", Name: "e1"}},
		IssuedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.Dial(ctx, a.listener.Addr().String(), client.Config{
		SpaceID:  "main",
		ClientID: "c1",
		Ticket:   tk,
		Entities: []connect.Entity{{ID: "e1", Name: "e1"}},
		TLS:      &tls.Config{InsecureSkipVerify: true},
	})
	if err == nil {
		t.Fatal("an unticketed space must reject a presented ticket")
	}
}

// TestRawQUICRejectionCarriesCode: over raw QUIC the §4.6 rejection
// code rides CONNECTION_CLOSE natively — the bridge's transport
// delivers "correctness rides the code alone" today. (The WebTransport
// counterpart is a known defect — see wt_test.go
// TestWTRejectionCarriesCode.)
func TestRawQUICRejectionCarriesCode(t *testing.T) {
	a, _ := startTicketedApp(t)
	_, err := dialTest(t, a, "c-nt", "e-nt") // no ticket into a ticketed space
	if err == nil {
		t.Fatal("unticketed dial into a ticketed space must fail")
	}
	var appErr *quic.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("rejection must surface the QUIC application error; got %T: %v", err, err)
	}
	if uint32(appErr.ErrorCode) != wire.ErrCodeInvalidTicket {
		t.Fatalf("close code = %#x, want %#x (reason %q)", uint64(appErr.ErrorCode), wire.ErrCodeInvalidTicket, appErr.ErrorMessage)
	}
}
