package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The operations endpoints answer from a live app, and readiness flips
// to 503 the moment shutdown begins.
func TestHTTPEndpoints(t *testing.T) {
	cfg := defaultConfig()
	cfg.Host, cfg.Port = "localhost", 0
	a, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	h := a.httpHandler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/healthz"); rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("/healthz = %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200 while serving", rec.Code)
	}
	rec := get("/stats")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("/stats = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var doc struct {
		Version  string         `json:"version"`
		Space    string         `json:"space"`
		Uptime   float64        `json:"uptime_seconds"`
		Ready    bool           `json:"ready"`
		Engine   map[string]any `json:"engine"`
		Entities []any          `json:"entities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version == "" || doc.Space != cfg.SpaceID || !doc.Ready || doc.Entities == nil {
		t.Fatalf("/stats body: %+v", doc)
	}
	if _, ok := doc.Engine["Ticks"]; !ok {
		t.Fatalf("/stats engine counters missing: %v", doc.Engine)
	}
	if rec := get("/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("/nope = %d", rec.Code)
	}
	if rec := httptest.NewRecorder(); true {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST /healthz = %d", rec.Code)
		}
	}

	// Shutdown begins: readiness drains, liveness holds, stats say so.
	a.ready.Store(false)
	if rec := get("/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz during shutdown = %d, want 503", rec.Code)
	}
	if rec := get("/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("/healthz during shutdown = %d, want 200", rec.Code)
	}
	if rec := get("/stats"); !json.Valid(rec.Body.Bytes()) || rec.Code != http.StatusOK {
		t.Fatalf("/stats during shutdown = %d", rec.Code)
	}
}

// With a port configured the server really listens on it.
func TestHTTPListens(t *testing.T) {
	cfg := defaultConfig()
	cfg.Host, cfg.Port = "localhost", 0
	a, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if err := a.serveHTTP("localhost:0"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + a.httpAddr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz over TCP = %d", resp.StatusCode)
	}
}
