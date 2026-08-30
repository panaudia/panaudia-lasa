package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// The operations endpoints (PANAUDIA_HTTP_PORT): what an orchestrator, a
// load balancer or a collector polls. Plain HTTP over TCP because that
// is what probes speak; the media port is UDP-only.
//
//	GET /healthz  200 while the process is up (liveness)
//	GET /readyz   200 while accepting sessions, 503 once shutdown begins
//	              (readiness — a balancer drains on it)
//	GET /stats    the stats snapshot as JSON: version, space, uptime,
//	              engine render counters, per-entity ingest diagnostics
func (a *app) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !a.ready.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		s := a.stats()
		out := statsDocument{
			Version:       serverVersion(),
			Space:         a.space,
			UptimeSeconds: time.Since(a.started).Seconds(),
			Ready:         a.ready.Load(),
			Engine:        s.Engine,
			Entities:      s.Entities,
			UDP:           s.UDP,
			Log:           s.Log,
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})
	return mux
}

// statsDocument is the /stats body: the serverStats snapshot plus the
// identity an operator wants beside it.
type statsDocument struct {
	Version       string        `json:"version"`
	Space         string        `json:"space"`
	UptimeSeconds float64       `json:"uptime_seconds"`
	Ready         bool          `json:"ready"`
	Engine        any           `json:"engine"`
	Entities      []entityStats `json:"entities"`
	UDP           udpBuffers    `json:"udp"`
	Log           logStats      `json:"log"`
}
