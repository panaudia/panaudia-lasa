package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
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

// Precedence is real environment > .env file > default, and the .env
// path resolves from PANAUDIA_ENV_FILE.
func TestDotEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.env")
	if err := os.WriteFile(path, []byte("PANAUDIA_SPACE=from-file\nPANAUDIA_PORT=5555\nPANAUDIA_ALLOW_UNTICKETED=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// godotenv only fills variables the process does not already have,
	// so make sure these are genuinely absent, then restore afterwards.
	for _, name := range []string{"PANAUDIA_SPACE", "PANAUDIA_PORT", "PANAUDIA_ALLOW_UNTICKETED"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Cleanup(func() { os.Setenv(name, v) })
		} else {
			t.Cleanup(func() { os.Unsetenv(name) })
		}
		os.Unsetenv(name)
	}
	t.Setenv("PANAUDIA_ENV_FILE", path)
	t.Setenv("PANAUDIA_PORT", "6666") // real environment must win over the file

	real := snapshotEnv()
	loaded, err := loadDotEnv()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != path {
		t.Fatalf("loaded %q, want %q", loaded, path)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SpaceID != "from-file" {
		t.Errorf("SpaceID = %q, want from-file (.env should fill an unset variable)", cfg.SpaceID)
	}
	if cfg.Port != 6666 {
		t.Errorf("Port = %d, want 6666 (real environment beats .env)", cfg.Port)
	}
	if got := provenance("PANAUDIA_PORT", real); got != "env" {
		t.Errorf("PANAUDIA_PORT provenance = %s, want env", got)
	}
	if got := provenance("PANAUDIA_SPACE", real); got != ".env" {
		t.Errorf("PANAUDIA_SPACE provenance = %s, want .env", got)
	}
	if got := provenance("PANAUDIA_REVERB", real); got != "default" {
		t.Errorf("PANAUDIA_REVERB provenance = %s, want default", got)
	}
	// Every variable has an effective value line, defaults included.
	eff := cfg.effective()
	for _, name := range configVars {
		if _, ok := eff[name]; !ok {
			t.Errorf("effective() lacks %s", name)
		}
	}
}

// A missing default .env is normal; a missing EXPLICIT one is an error.
func TestDotEnvMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PANAUDIA_ENV_FILE", "")
	if p, err := loadDotEnv(); err != nil || p != "" {
		t.Fatalf("default .env absent: got (%q, %v), want (\"\", nil)", p, err)
	}
	t.Setenv("PANAUDIA_ENV_FILE", "nope.env")
	if _, err := loadDotEnv(); err == nil {
		t.Fatal("explicit missing PANAUDIA_ENV_FILE must be an error")
	}
}

// PANAUDIA_HTTP_PORT: off by default, a port when set, garbage refused.
func TestLoadConfigHTTPPort(t *testing.T) {
	t.Setenv("PANAUDIA_ALLOW_UNTICKETED", "true")
	t.Setenv("PANAUDIA_HTTP_PORT", "")
	cfg, err := loadConfig()
	if err != nil || cfg.HTTPPort != 0 {
		t.Fatalf("default: port %d err %v, want 0 nil", cfg.HTTPPort, err)
	}
	t.Setenv("PANAUDIA_HTTP_PORT", "8080")
	if cfg, err = loadConfig(); err != nil || cfg.HTTPPort != 8080 {
		t.Fatalf("set: port %d err %v", cfg.HTTPPort, err)
	}
	for _, bad := range []string{"x", "-1", "70000"} {
		t.Setenv("PANAUDIA_HTTP_PORT", bad)
		if _, err := loadConfig(); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
