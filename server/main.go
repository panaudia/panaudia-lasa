// panaudia-server composes the lasa protocol shell and the
// panaudia-engine spatial renderer into the standalone LASA server:
// one process, one space, one clock (the render tick).
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"

	"github.com/panaudia/moqtransport/quicmoq"

	"github.com/panaudia/lasa/profile/base"
	"github.com/panaudia/lasa/server"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// app is the composed server: mixer + clock + adapter + shell. main
// builds one from the env; tests build one directly.
type app struct {
	mixer    *engine.Mixer
	backend  *backend
	srv      *server.Server
	wt       *webtransport.Server
	listener *quic.Listener
	certHash string // dev-cert serverCertificateHashes value; "" with real certs
}

func newApp(cfg appConfig) (*app, error) {
	mixer, err := engine.New(cfg.engineConfig())
	if err != nil {
		return nil, err
	}
	clk := &sampleClock{}
	b := newBackend(mixer)
	var keys []ed25519.PublicKey
	if cfg.TicketKey != nil {
		keys = append(keys, cfg.TicketKey)
	}
	srv, err := server.New(server.Config{
		SpaceID:     cfg.SpaceID,
		Backend:     b,
		SampleEpoch: clk.sampleEpoch,
		// The sink offering derives from the bus order inside the shell
		// (P4: one order knob): ambi2 always, ambi3 at order 3+.
		AmbiOrder: cfg.Order,
		// Access policy is the base profile's: roles expand to rules,
		// unknown role names reject the ticket, admin/presenter may
		// carry ad-hoc entities, admin/producer may listen through any
		// entity's ears.
		TicketKeys:      keys,
		AllowUnticketed: cfg.AllowUnticketed,
		// Capacity adjudicates with admission at the state engine —
		// every admission refusal issues from one place.
		Capacity:       cfg.MaxEntities,
		AdHocPermitted: base.AdHocPermitted,
		SinkAll:        base.SinkAll,
		ValidRoles:     base.ValidRoles,
		RulesFor:       base.RulesFor,
	})
	if err != nil {
		mixer.Close()
		return nil, err
	}
	b.attach(srv)
	// The profile consumer (design F): observer → base.ParseOp → engine
	// mutators. Registered before serving, so it sees every op from the
	// first admission on (the replay covers the then-empty store).
	w := newStateWiring(mixer, b)
	b.wiring = w
	if err := srv.Engine().Observe(w.prefixes(), w.observe); err != nil {
		srv.Close()
		mixer.Close()
		return nil, err
	}
	tlsCfg, certHash, err := serverTLS(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		srv.Close()
		mixer.Close()
		return nil, err
	}
	l, err := quic.ListenAddr(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), tlsCfg, &quic.Config{
		EnableDatagrams: true,
		// WebTransport requires partial-delivery resets on both ends.
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		srv.Close()
		mixer.Close()
		return nil, err
	}
	a := &app{
		mixer: mixer, backend: b, srv: srv,
		wt:       newWebTransport(srv, tlsCfg),
		listener: l,
		certHash: certHash,
	}
	go clk.run(mixer)
	return a, nil
}

// serve accepts on the one UDP listener and routes each connection by
// its negotiated ALPN: raw-QUIC MoQ straight into the shell,
// HTTP/3 into the WebTransport upgrade path (wt.go) — which lands in
// the same shell.
func (a *app) serve() error {
	for {
		conn, err := a.listener.Accept(context.Background())
		if err != nil {
			return err
		}
		switch conn.ConnectionState().TLS.NegotiatedProtocol {
		case server.ALPN:
			go func() { _ = a.srv.ServeConn(conn.Context(), quicmoq.NewServer(conn)) }()
		case http3.NextProtoH3:
			go func() {
				if err := a.wt.ServeQUICConn(conn); err != nil {
					log.Printf("webtransport: h3 serve: %v", err)
				}
			}()
		default:
			_ = conn.CloseWithError(0, "unsupported ALPN")
		}
	}
}

func (a *app) close() {
	_ = a.listener.Close()
	_ = a.wt.Close()
	a.srv.Close()
	a.mixer.Close()
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	a, err := newApp(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer a.close()
	go a.statsLoop(time.Duration(cfg.StatsSec) * time.Second)
	log.Printf("panaudia-server: space %q on %s (order %d, ambi offering up to %d, max %d entities)",
		cfg.SpaceID, a.listener.Addr(), cfg.Order, min(cfg.Order, 3), cfg.MaxEntities)
	log.Printf("panaudia-server: WebTransport at https://<host>:%d%s", cfg.Port, wtPath)
	if a.certHash != "" {
		log.Printf("panaudia-server: dev cert serverCertificateHashes (sha-256, base64): %s", a.certHash)
	}
	if err := a.serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
