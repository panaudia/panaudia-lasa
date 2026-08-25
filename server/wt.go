package main

// WebTransport (Phase D1): the browser transport. One HTTP/3 server
// whose only endpoint upgrades to WebTransport and hands the session to
// the SAME lasa shell as raw QUIC — "identical over WebTransport and
// raw QUIC" (lasa-core §4.1) is structural: ServeConn neither knows nor
// cares. Both transports share the one UDP listener; the serve loop
// routes by negotiated ALPN (h3 here, moqt-16 to the raw-QUIC path).

import (
	"crypto/tls"
	"log/slog"
	"net/http"

	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"

	"github.com/panaudia/moqtransport/webtransportmoq"

	"github.com/panaudia/lasa/server"
)

// wtPath is the WebTransport upgrade endpoint.
const wtPath = "/lasa"

// newWebTransport builds the WebTransport server around the shell.
func newWebTransport(srv *server.Server, tlsCfg *tls.Config) *webtransport.Server {
	mux := http.NewServeMux()
	wt := &webtransport.Server{
		H3: &http3.Server{
			TLSConfig:       tlsCfg,
			Handler:         mux,
			EnableDatagrams: true,
		},
		// Browsers may negotiate the MoQ ALPN as a WebTransport
		// application protocol (the SDK offers it with a no-protocol
		// fallback for browsers that lack the API).
		ApplicationProtocols: []string{server.ALPN},
		// Any web origin may connect: LASA authenticates with the
		// ticket in the Connection Config, never with browser-ambient
		// credentials, and third-party apps on arbitrary domains are
		// the product. (The library default is same-origin.)
		CheckOrigin: func(*http.Request) bool { return true },
	}
	mux.HandleFunc(wtPath, func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("webtransport: upgrade request", "remote", r.RemoteAddr, "protocols", r.Header.Get("Wt-Available-Protocols"))
		sess, err := wt.Upgrade(w, r)
		if err != nil {
			slog.Warn("webtransport: upgrade failed", "remote", r.RemoteAddr, "err", err)
			http.Error(w, "webtransport upgrade failed", http.StatusBadRequest)
			return
		}
		slog.Info("webtransport: session established", "remote", r.RemoteAddr)
		// Session.Context ends when the transport dies — the guaranteed
		// teardown signal ServeConn requires (invariant L5).
		go func() { _ = srv.ServeConn(sess.Context(), webtransportmoq.NewServer(sess)) }()
	})
	return wt
}
