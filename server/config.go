package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// appConfig is the whole env surface. Everything has a dev-friendly
// default; PANAUDIA_CERT/PANAUDIA_KEY unset means an ephemeral
// self-signed certificate, and PANAUDIA_TICKET_KEY unset means an
// unticketed space (both dev mode).
type appConfig struct {
	Host     string // PANAUDIA_HOST (bind address; default all interfaces)
	Port     int    // PANAUDIA_PORT (UDP; default 4443)
	SpaceID  string // PANAUDIA_SPACE (default "main")
	CertFile string // PANAUDIA_CERT
	KeyFile  string // PANAUDIA_KEY
	Order    int    // PANAUDIA_ORDER: internal ambisonic bus order 2–5 (default 3);
	// the ambi sink offering derives from it (ambi2 always, ambi3 when ≥ 3)
	MaxEntities int    // PANAUDIA_MAX_ENTITIES (default 64)
	Workers     int    // PANAUDIA_WORKERS (default engine's)
	Reverb      string // PANAUDIA_REVERB (default "medium-room")

	// StatsSec is the stats-log cadence in seconds — PANAUDIA_STATS_SEC,
	// default 60. At 15 s or faster the loop also logs a per-entity
	// detail line (ingest jitter snapshot + depacketizer breakdown) —
	// the jitter-debugging mode; the default cadence keeps the compact
	// aggregate line only.
	StatsSec int

	// TicketKey is the space's ONE Ed25519 public key for ticket
	// verification — PANAUDIA_TICKET_KEY, base64 of the raw 32 bytes.
	// The server is verification-only: issuance is out-of-band by
	// design and the private key never touches it. A space is either
	// ticketed (key configured) or open (PANAUDIA_ALLOW_UNTICKETED=true,
	// an explicit opt-in so a missing key can never silently admit
	// anyone); loadConfig rejects both-or-neither at startup.
	TicketKey       ed25519.PublicKey
	AllowUnticketed bool
}

var reverbPresets = map[string]int{
	"none":        engine.ReverbNone,
	"tight-room":  engine.ReverbTightRoom,
	"small-room":  engine.ReverbSmallRoom,
	"medium-room": engine.ReverbMediumRoom,
	"large-hall":  engine.ReverbLargeHall,
	"cathedral":   engine.ReverbCathedral,
}

func defaultConfig() appConfig {
	e := engine.DefaultConfig()
	return appConfig{
		Port:            4443,
		SpaceID:         "main",
		Order:           e.Order,
		MaxEntities:     e.MaxEntities,
		Workers:         e.Workers,
		Reverb:          "medium-room",
		StatsSec:        60,
		AllowUnticketed: true, // in-process test convenience; the env path (loadConfig) demands an explicit choice
	}
}

func loadConfig() (appConfig, error) {
	cfg := defaultConfig()
	var err error
	cfg.Host = os.Getenv("PANAUDIA_HOST")
	if cfg.Port, err = envInt("PANAUDIA_PORT", cfg.Port); err != nil {
		return cfg, err
	}
	if v := os.Getenv("PANAUDIA_SPACE"); v != "" {
		cfg.SpaceID = v
	}
	cfg.CertFile = os.Getenv("PANAUDIA_CERT")
	cfg.KeyFile = os.Getenv("PANAUDIA_KEY")
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return cfg, fmt.Errorf("PANAUDIA_CERT and PANAUDIA_KEY must be set together")
	}
	if cfg.Order, err = envInt("PANAUDIA_ORDER", cfg.Order); err != nil {
		return cfg, err
	}
	if cfg.MaxEntities, err = envInt("PANAUDIA_MAX_ENTITIES", cfg.MaxEntities); err != nil {
		return cfg, err
	}
	if cfg.Workers, err = envInt("PANAUDIA_WORKERS", cfg.Workers); err != nil {
		return cfg, err
	}
	if v := os.Getenv("PANAUDIA_REVERB"); v != "" {
		cfg.Reverb = v
	}
	if cfg.StatsSec, err = envInt("PANAUDIA_STATS_SEC", cfg.StatsSec); err != nil {
		return cfg, err
	}
	if cfg.StatsSec < 1 {
		return cfg, fmt.Errorf("PANAUDIA_STATS_SEC: must be >= 1, got %d", cfg.StatsSec)
	}
	if _, ok := reverbPresets[cfg.Reverb]; !ok {
		return cfg, fmt.Errorf("PANAUDIA_REVERB: unknown preset %q", cfg.Reverb)
	}
	if v := os.Getenv("PANAUDIA_TICKET_KEY"); v != "" {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return cfg, fmt.Errorf("PANAUDIA_TICKET_KEY: %w", err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return cfg, fmt.Errorf("PANAUDIA_TICKET_KEY: want %d key bytes, got %d", ed25519.PublicKeySize, len(raw))
		}
		cfg.TicketKey = ed25519.PublicKey(raw)
	}
	// A space is either ticketed or open — never both, and never open
	// by accident: a missing ticket key must not silently admit anyone,
	// so an open space requires the explicit opt-in.
	cfg.AllowUnticketed = false
	if v := os.Getenv("PANAUDIA_ALLOW_UNTICKETED"); v != "" {
		if cfg.AllowUnticketed, err = strconv.ParseBool(v); err != nil {
			return cfg, fmt.Errorf("PANAUDIA_ALLOW_UNTICKETED: %w", err)
		}
	}
	switch {
	case cfg.TicketKey != nil && cfg.AllowUnticketed:
		return cfg, fmt.Errorf("PANAUDIA_TICKET_KEY with PANAUDIA_ALLOW_UNTICKETED=true: a space is either ticketed or open, not both")
	case cfg.TicketKey == nil && !cfg.AllowUnticketed:
		return cfg, fmt.Errorf("no PANAUDIA_TICKET_KEY: configure one, or explicitly opt into an open space with PANAUDIA_ALLOW_UNTICKETED=true")
	}
	return cfg, nil
}

func (c appConfig) engineConfig() engine.Config {
	return engine.Config{
		Order:        c.Order,
		MaxEntities:  c.MaxEntities,
		ReverbPreset: reverbPresets[c.Reverb],
		Workers:      c.Workers,
	}
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}
