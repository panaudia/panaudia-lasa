package main

// D1 acceptance: WebTransport into the same shell as raw QUIC, routed
// by ALPN off the one UDP listener — a WT client and a raw-QUIC client
// share a space and hear each other.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"testing"
	"time"

	webtransport "github.com/quic-go/webtransport-go"

	"github.com/panaudia/lasa/client"
	"github.com/panaudia/lasa/connect"
	"github.com/panaudia/lasa/wire"
)

func dialWT(t *testing.T, a *app, clientID string, entityIDs ...string) (*client.Client, error) {
	t.Helper()
	ents := make([]connect.Entity, len(entityIDs))
	for i, id := range entityIDs {
		ents[i] = connect.Entity{ID: id, Name: id}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("https://%s%s", a.listener.Addr(), wtPath)
	return client.DialWebTransport(ctx, url, client.Config{
		SpaceID:  "main",
		ClientID: clientID,
		Entities: ents,
		TLS:      &tls.Config{InsecureSkipVerify: true},
	})
}

// TestWebTransportE2E: full duplex over WebTransport beside raw QUIC.
// The WT client speaks; the raw-QUIC client hears it; the WT client
// hears the raw-QUIC client back — every path (ingress datagrams,
// sink egress, state, presence subscription plumbing) crosses the WT
// session at least once.
func TestWebTransportE2E(t *testing.T) {
	a := startTestApp(t, func(cfg *appConfig) { cfg.Reverb = "none" })

	// Dev cert must expose the browser hash.
	if a.certHash == "" {
		t.Fatal("dev boot must carry a serverCertificateHashes value")
	}

	wt, err := dialWT(t, a, "c-wt", "e-wt")
	if err != nil {
		t.Fatalf("WebTransport dial: %v", err)
	}
	defer wt.Close()
	q, err := dialTest(t, a, "c-q", "e-q")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	waitFor(t, "both transports admitted", func() bool { return settled(a, "e-wt", "e-q") })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)

	// Both speak; each hears the other through their own sink.
	speakToneAt(t, ctx, wt, "e-wt", 440, 0.1, wire.Pose{Y: 1}, stop)
	speakToneAt(t, ctx, q, "e-q", 700, 0.1, wire.Pose{Y: -1}, stop)

	qMeter := newSinkMeter(t, ctx, q, "e-q")
	wtMeter := newSinkMeter(t, ctx, wt, "e-wt")
	for i := 0; i < 60; i++ {
		qMeter.next(t, ctx, "raw-QUIC warm-up")
		wtMeter.next(t, ctx, "WT warm-up")
	}
	qMeter.waitLevel(t, ctx, "raw-QUIC client hears the WT speaker", 10, func(r float64) bool { return r > 0.005 })
	wtMeter.waitLevel(t, ctx, "WT client hears the raw-QUIC speaker", 10, func(r float64) bool { return r > 0.005 })
}

// TestWTRejectionCarriesCode: a rejected CLIENT_SETUP over WebTransport
// must deliver the §4.6 code to the client — lasa-core.md §4.6:
// "correctness MUST ride the code alone". Exercised with the Go WT
// client to isolate the server stack from browser behaviour.
func TestWTRejectionCarriesCode(t *testing.T) {
	a, _ := startTicketedApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("https://%s%s", a.listener.Addr(), wtPath)
	_, err := client.DialWebTransport(ctx, url, client.Config{
		SpaceID:  "main",
		ClientID: "c-nt",
		Entities: []connect.Entity{{ID: "e-nt", Name: "e-nt"}},
		TLS:      &tls.Config{InsecureSkipVerify: true},
	})
	if err == nil {
		t.Fatal("unticketed dial into a ticketed space must fail")
	}
	var sessErr *webtransport.SessionError
	if !errors.As(err, &sessErr) {
		t.Fatalf("rejection must surface as a WT session error carrying the code; got %T: %v", err, err)
	}
	if uint32(sessErr.ErrorCode) != wire.ErrCodeInvalidTicket {
		t.Fatalf("close code = %#x, want %#x (reason %q)", uint32(sessErr.ErrorCode), wire.ErrCodeInvalidTicket, sessErr.Message)
	}
}
