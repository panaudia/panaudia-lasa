package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// This file is the ONLY place the process reads its environment
// (pinned by TestNoEnvReadsOutsideConfig). Everything is assessed once
// in main — .env, then loadConfig — and passed on as appConfig.
//
// Precedence: real environment > .env file > defaults. The full
// effective configuration, with each value's provenance, is logged at
// startup (logEffective); .env.example beside this file documents every
// variable.

// appConfig is the whole env surface. Everything has a dev-friendly
// default; PANAUDIA_CERT/PANAUDIA_KEY unset means an ephemeral
// self-signed certificate, and PANAUDIA_TICKET_KEY unset means an
// unticketed space (both dev mode).
type appConfig struct {
	Host     string // PANAUDIA_HOST (bind address; default all interfaces)
	Port     int    // PANAUDIA_PORT (UDP; default 4443)
	HTTPPort int    // PANAUDIA_HTTP_PORT (TCP; health/readiness/stats; 0 = off, the default)
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

	// Logging: one slog handler on stderr, installed by main before
	// anything else runs. PANAUDIA_LOG_LEVEL is debug|info|warn|error
	// (default info); PANAUDIA_LOG_FORMAT is text (default, for a
	// terminal) or json (one object per line, for a log collector).
	LogLevel  slog.Level
	LogFormat string

	// MixerGonum forces the engine's pure-Go mixing path over the GEMM
	// backend — PANAUDIA_MIXER_GONUM=true, an A/B and benchmark hatch,
	// never needed in production.
	MixerGonum bool
}

// configVars lists every environment variable the server reads, in the
// order the startup printout uses. Keep .env.example in step.
var configVars = []string{
	"PANAUDIA_HOST", "PANAUDIA_PORT", "PANAUDIA_HTTP_PORT", "PANAUDIA_SPACE",
	"PANAUDIA_CERT", "PANAUDIA_KEY",
	"PANAUDIA_TICKET_KEY", "PANAUDIA_ALLOW_UNTICKETED",
	"PANAUDIA_ORDER", "PANAUDIA_MAX_ENTITIES", "PANAUDIA_WORKERS", "PANAUDIA_REVERB",
	"PANAUDIA_STATS_SEC", "PANAUDIA_LOG_LEVEL", "PANAUDIA_LOG_FORMAT",
	"PANAUDIA_MIXER_GONUM",
}

// inheritedUDPFDVar is not configuration: it is the handoff from
// udpPrelude (udpbuf_linux.go) to the re-exec'd process, carrying the
// fd of the already-bound, already-sized media socket. Read once here
// (the environment is read in this file only) and removed from the
// environment so nothing downstream inherits it. Deliberately absent
// from configVars and .env.example; setting it by hand is unsupported.
const inheritedUDPFDVar = "PANAUDIA_INHERITED_UDP_FD"

var inheritedUDPFDOnce struct {
	done bool
	fd   int
}

// inheritedUDPFD returns the handed-over socket fd, or -1.
func inheritedUDPFD() int {
	if !inheritedUDPFDOnce.done {
		inheritedUDPFDOnce.done = true
		inheritedUDPFDOnce.fd = -1
		if v := os.Getenv(inheritedUDPFDVar); v != "" {
			_ = os.Unsetenv(inheritedUDPFDVar)
			if fd, err := strconv.Atoi(v); err == nil && fd >= 0 {
				inheritedUDPFDOnce.fd = fd
			}
		}
	}
	return inheritedUDPFDOnce.fd
}

// environWith is the current environment plus one variable, for the
// re-exec in udpPrelude.
func environWith(name, value string) []string {
	return append(os.Environ(), name+"="+value)
}

// envSnapshot records which config variables the REAL environment sets,
// taken before the .env file is loaded so provenance can be reported.
type envSnapshot map[string]bool

func snapshotEnv() envSnapshot {
	s := envSnapshot{}
	for _, name := range configVars {
		if _, ok := os.LookupEnv(name); ok {
			s[name] = true
		}
	}
	return s
}

// loadDotEnv loads PANAUDIA_ENV_FILE if set, else ./.env, into the
// process environment without overriding variables already set (so the
// real environment wins). A missing file is normal and returns "";
// a malformed one is an error — config loading fails fast. Returns the
// path actually loaded.
func loadDotEnv() (string, error) {
	path := os.Getenv("PANAUDIA_ENV_FILE")
	explicit := path != ""
	if !explicit {
		path = ".env"
	}
	if err := godotenv.Load(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return "", nil
		}
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return path, nil
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
		LogLevel:        slog.LevelInfo,
		LogFormat:       "text",
	}
}

func loadConfig() (appConfig, error) {
	cfg := defaultConfig()
	var err error
	cfg.Host = os.Getenv("PANAUDIA_HOST")
	if cfg.Port, err = envInt("PANAUDIA_PORT", cfg.Port); err != nil {
		return cfg, err
	}
	if cfg.HTTPPort, err = envInt("PANAUDIA_HTTP_PORT", cfg.HTTPPort); err != nil {
		return cfg, err
	}
	if cfg.HTTPPort < 0 || cfg.HTTPPort > 65535 {
		return cfg, fmt.Errorf("PANAUDIA_HTTP_PORT: must be 0 (off) or a port, got %d", cfg.HTTPPort)
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
	if v := os.Getenv("PANAUDIA_LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return cfg, fmt.Errorf("PANAUDIA_LOG_LEVEL: want debug|info|warn|error, got %q", v)
		}
	}
	if v := os.Getenv("PANAUDIA_LOG_FORMAT"); v != "" {
		cfg.LogFormat = strings.ToLower(v)
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return cfg, fmt.Errorf("PANAUDIA_LOG_FORMAT: want text|json, got %q", cfg.LogFormat)
	}
	if v := os.Getenv("PANAUDIA_MIXER_GONUM"); v != "" {
		if cfg.MixerGonum, err = strconv.ParseBool(v); err != nil {
			return cfg, fmt.Errorf("PANAUDIA_MIXER_GONUM: %w", err)
		}
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
		PureGoMixer:  c.MixerGonum,
	}
}

// effective renders every variable's effective value — defaults
// included — as the startup printout shows it.
func (c appConfig) effective() map[string]string {
	ticketKey := ""
	if c.TicketKey != nil {
		ticketKey = base64.StdEncoding.EncodeToString(c.TicketKey)
	}
	return map[string]string{
		"PANAUDIA_HOST":             c.Host,
		"PANAUDIA_PORT":             strconv.Itoa(c.Port),
		"PANAUDIA_HTTP_PORT":        strconv.Itoa(c.HTTPPort),
		"PANAUDIA_SPACE":            c.SpaceID,
		"PANAUDIA_CERT":             c.CertFile,
		"PANAUDIA_KEY":              c.KeyFile,
		"PANAUDIA_TICKET_KEY":       ticketKey,
		"PANAUDIA_ALLOW_UNTICKETED": strconv.FormatBool(c.AllowUnticketed),
		"PANAUDIA_ORDER":            strconv.Itoa(c.Order),
		"PANAUDIA_MAX_ENTITIES":     strconv.Itoa(c.MaxEntities),
		"PANAUDIA_WORKERS":          strconv.Itoa(c.Workers),
		"PANAUDIA_REVERB":           c.Reverb,
		"PANAUDIA_STATS_SEC":        strconv.Itoa(c.StatsSec),
		"PANAUDIA_LOG_LEVEL":        strings.ToLower(c.LogLevel.String()),
		"PANAUDIA_LOG_FORMAT":       c.LogFormat,
		"PANAUDIA_MIXER_GONUM":      strconv.FormatBool(c.MixerGonum),
	}
}

// provenance says where a variable's effective value came from: the
// real environment, the .env file, or the default.
func provenance(name string, real envSnapshot) string {
	switch {
	case real[name]:
		return "env"
	case os.Getenv(name) != "":
		return ".env"
	default:
		return "default"
	}
}

// logEffective prints the full effective configuration, one line per
// variable, so what the process believes is never in doubt.
func (c appConfig) logEffective(real envSnapshot, dotEnvPath string) {
	if dotEnvPath != "" {
		slog.Info("config: loaded .env file", "path", dotEnvPath)
	} else {
		slog.Info("config: no .env file (PANAUDIA_ENV_FILE unset, no ./.env)")
	}
	values := c.effective()
	for _, name := range configVars {
		v := values[name]
		if v == "" {
			v = "(unset)"
		}
		slog.Info("config", "name", name, "value", v, "source", provenance(name, real))
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
