// panaudia-server composes the lasa protocol shell and the
// panaudia-engine spatial renderer into the standalone LASA server:
// one process, one space, one clock (the render tick).
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
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

	space    string
	started  time.Time
	ready    atomic.Bool  // false once shutdown begins: /readyz answers 503
	httpSrv  *http.Server // the operations endpoints, nil when PANAUDIA_HTTP_PORT is 0
	httpAddr string       // its bound address, for logs and tests
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
		space:    cfg.SpaceID,
		started:  time.Now(),
	}
	a.ready.Store(true)
	go clk.run(mixer)
	return a, nil
}

// serveHTTP starts the operations endpoints on a TCP listener. Off
// unless configured; separate from the media port on purpose, so it
// can be firewalled to the private network.
func (a *app) serveHTTP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	a.httpSrv = &http.Server{Handler: a.httpHandler(), ReadHeaderTimeout: 5 * time.Second}
	a.httpAddr = ln.Addr().String()
	go func() {
		if err := a.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("http: serve", "err", err)
		}
	}()
	slog.Info("panaudia-server: http endpoints", "addr", ln.Addr().String(), "paths", "/healthz /readyz /stats")
	return nil
}

// serve accepts on the one UDP listener and routes each connection by
// its negotiated ALPN: raw-QUIC MoQ straight into the shell,
// HTTP/3 into the WebTransport upgrade path (wt.go) — which lands in
// the same shell. Returns when ctx ends (the shutdown signal) or the
// listener fails.
func (a *app) serve(ctx context.Context) error {
	for {
		conn, err := a.listener.Accept(ctx)
		if err != nil {
			return err
		}
		switch conn.ConnectionState().TLS.NegotiatedProtocol {
		case server.ALPN:
			go func() { _ = a.srv.ServeConn(conn.Context(), quicmoq.NewServer(conn)) }()
		case http3.NextProtoH3:
			go func() {
				if err := a.wt.ServeQUICConn(conn); err != nil {
					slog.Warn("webtransport: h3 serve", "err", err)
				}
			}()
		default:
			_ = conn.CloseWithError(0, "unsupported ALPN")
		}
	}
}

func (a *app) close() {
	a.ready.Store(false)
	_ = a.listener.Close()
	_ = a.wt.Close()
	a.srv.Close()
	a.mixer.Close()
	if a.httpSrv != nil {
		_ = a.httpSrv.Close()
	}
}

// shutdownGrace bounds the orderly stop: how long to wait for every
// live session's coded close and departure funnel before closing the
// engine regardless.
const shutdownGrace = 5 * time.Second

// shutdown is the orderly stop: stop accepting, close every live
// session with the shutting-down code (clients can tell a stop from a
// dead network) and wait for their departures, then release the rest.
func (a *app) shutdown() {
	a.ready.Store(false) // first: a balancer polling /readyz stops routing here
	_ = a.listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := a.srv.Shutdown(ctx); err != nil {
		slog.Warn("shutdown: not every session finished within the grace period", "grace", shutdownGrace, "err", err)
	}
	_ = a.wt.Close()
	a.mixer.Close()
	if a.httpSrv != nil {
		_ = a.httpSrv.Shutdown(ctx) // last: /healthz stays answerable until the end
	}
}

// version is the release the binary reports. The git tag is the one
// source: docker/build sets it with -ldflags "-X main.version=…" from
// `git describe --match 'server/v*'`; a plain `go build` from a checkout
// tagged server/vX.Y.Z gets the same answer from the Go build info
// (Go 1.24+ derives Main.Version from the VCS tag); anything else is
// "dev". There is no version file to forget to bump.
var version string

func serverVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

// printBanner is the terminal greeting, stdout, text mode only: in JSON
// log mode every line the process emits must be an object a collector
// can parse, and the version is on the "listening" log line anyway.
func printBanner() {
	fmt.Printf("\n-----------------------------------------------------------------\n")
	fmt.Printf("\n\n")
	fmt.Printf("   ______   ___   _   _   ___   _   _ ______  _____  ___ \n")
	fmt.Printf("   | ___ \\ / _ \\ | \\ | | / _ \\ | | | ||  _  \\|_   _|/ _ \\ \n")
	fmt.Printf("   | |_/ // /_\\ \\|  \\| |/ /_\\ \\| | | || | | |  | | / /_\\ \\\n")
	fmt.Printf("   |  __/ |  _  || . ` ||  _  || | | || | | |  | | |  _  |\n")
	fmt.Printf("   | |    | | | || |\\  || | | || |_| || |/ /  _| |_| | | |\n")
	fmt.Printf("   \\_|    \\_| |_/\\_| \\_/\\_| |_/ \\___/ |___/   \\___/\\_| |_/\n\n")

	fmt.Printf("                  _       ___   _____   ___   \n")
	fmt.Printf("                 | |     / _ \\ /  ___| / _ \\  \n")
	fmt.Printf("                 | |    / /_\\ \\\\ `--. / /_\\ \\  \n")
	fmt.Printf("                 | |    |  _  | `--. \\|  _  |  \n")
	fmt.Printf("                 | |____| | | |/\\__/ /| | | |  \n")
	fmt.Printf("                 \\_____/\\_| |_/\\____/ \\_| |_/  \n\n")

	fmt.Printf("\n               --- Live Atomic Spatial Audio ---\n")
	fmt.Printf("\n                      https://panaudia.com\n")

	fmt.Printf("\n                            v%s \n\n", serverVersion())

	fmt.Printf("-----------------------------------------------------------------\n\n")
}

func main() {
	// Config first, logger second, everything else after: every line the
	// process emits goes through the configured handler. The environment
	// is read here and nowhere else (config.go).
	real := snapshotEnv()
	dotEnvPath, err := loadDotEnv()
	if err != nil {
		slog.Error("config: .env", "err", err)
		os.Exit(1)
	}
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	slog.SetDefault(newLogger(cfg, os.Stderr))
	if cfg.LogFormat == "text" {
		printBanner()
	}
	cfg.logEffective(real, dotEnvPath)
	a, err := newApp(cfg)
	if err != nil {
		slog.Error("start", "err", err)
		os.Exit(1)
	}
	go a.statsLoop(time.Duration(cfg.StatsSec) * time.Second)
	if cfg.HTTPPort > 0 {
		if err := a.serveHTTP(fmt.Sprintf("%s:%d", cfg.Host, cfg.HTTPPort)); err != nil {
			slog.Error("start: http endpoints", "err", err)
			a.close()
			os.Exit(1)
		}
	}
	slog.Info("panaudia-server: listening",
		"version", serverVersion(),
		"space", cfg.SpaceID, "addr", a.listener.Addr().String(),
		"order", cfg.Order, "ambiOfferingUpTo", min(cfg.Order, 3), "maxEntities", cfg.MaxEntities,
		"webtransport", fmt.Sprintf("https://<host>:%d%s", cfg.Port, wtPath))
	if a.certHash != "" {
		slog.Info("panaudia-server: dev cert serverCertificateHashes (sha-256, base64)", "hash", a.certHash)
	}
	// SIGINT/SIGTERM (Ctrl-C, docker stop, an orchestrator's rolling
	// restart) end the accept loop; the sessions are then closed with a
	// code before the process exits, instead of the runtime's default
	// of exiting mid-frame with every client left guessing.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = a.serve(ctx)
	if ctx.Err() != nil {
		slog.Info("panaudia-server: shutting down on signal", "grace", shutdownGrace)
		a.shutdown()
		slog.Info("panaudia-server: stopped")
		return
	}
	slog.Error("serve", "err", err)
	a.close()
	os.Exit(1)
}
