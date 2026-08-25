package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

// TestLoadConfigTicketPolicy: a space is either ticketed or open,
// decided explicitly — both-or-neither is a startup error, so a
// missing ticket key can never silently admit anyone.
func TestLoadConfigTicketPolicy(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	cases := []struct {
		name, key, allow string
		wantErr          bool
		wantOpen         bool
	}{
		{"neither: missing key must not open the space", "", "", true, false},
		{"open is an explicit opt-in", "", "true", false, true},
		{"ticketed", key, "", false, false},
		{"ticketed with explicit false", key, "false", false, false},
		{"both is contradictory", key, "true", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PANAUDIA_TICKET_KEY", c.key)
			t.Setenv("PANAUDIA_ALLOW_UNTICKETED", c.allow)
			cfg, err := loadConfig()
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && cfg.AllowUnticketed != c.wantOpen {
				t.Errorf("AllowUnticketed = %v, want %v", cfg.AllowUnticketed, c.wantOpen)
			}
		})
	}
}

// TestLoadConfigLogging: level and format parse from the env with the
// documented defaults, and a typo is a startup error rather than a
// silently different logger.
func TestLoadConfigLogging(t *testing.T) {
	t.Setenv("PANAUDIA_ALLOW_UNTICKETED", "true")
	cases := []struct {
		name, level, format string
		wantErr             bool
		wantLevel           string
		wantFormat          string
	}{
		{"defaults", "", "", false, "INFO", "text"},
		{"debug json", "debug", "json", false, "DEBUG", "json"},
		{"case-insensitive", "WARN", "JSON", false, "WARN", "json"},
		{"bad level", "loud", "", true, "", ""},
		{"bad format", "", "xml", true, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PANAUDIA_LOG_LEVEL", c.level)
			t.Setenv("PANAUDIA_LOG_FORMAT", c.format)
			cfg, err := loadConfig()
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err != nil {
				return
			}
			if got := cfg.LogLevel.String(); got != c.wantLevel {
				t.Errorf("LogLevel = %s, want %s", got, c.wantLevel)
			}
			if cfg.LogFormat != c.wantFormat {
				t.Errorf("LogFormat = %q, want %q", cfg.LogFormat, c.wantFormat)
			}
		})
	}
}
