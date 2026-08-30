package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/logging"
)

type captureLogger struct{ entries []logging.Entry }

func (c *captureLogger) Log(e logging.Entry) { c.entries = append(c.entries, e) }

func cloudTestLogger(level slog.Level) (*slog.Logger, *captureLogger, *cloudSink) {
	cap := &captureLogger{}
	sink := &cloudSink{lg: cap, fallback: &bytes.Buffer{}}
	h := slog.NewJSONHandler(sink, cloudHandlerOptions(&slog.HandlerOptions{Level: level}))
	return slog.New(h), cap, sink
}

// One slog record becomes one entry: severity and timestamp lifted out
// of the payload, msg renamed to message, attrs and groups as the JSON
// handler renders them.
func TestCloudSinkEntryShape(t *testing.T) {
	lg, cap, _ := cloudTestLogger(slog.LevelDebug)
	lg.Info("panaudia-server: listening", "space", "main", "order", 3)
	lg.WithGroup("udp").Warn("short", "recv_bytes", 212992)
	lg.With("entity", "e1").Error("decode", "err", "bad packet")
	lg.Debug("fine")

	if len(cap.entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(cap.entries))
	}
	wantSev := []logging.Severity{logging.Info, logging.Warning, logging.Error, logging.Debug}
	for i, e := range cap.entries {
		if e.Severity != wantSev[i] {
			t.Errorf("entry %d severity = %v, want %v", i, e.Severity, wantSev[i])
		}
		if e.Timestamp.IsZero() || time.Since(e.Timestamp) > time.Minute {
			t.Errorf("entry %d timestamp = %v, want lifted from the record", i, e.Timestamp)
		}
		p, ok := e.Payload.(map[string]any)
		if !ok {
			t.Fatalf("entry %d payload %T, want object", i, e.Payload)
		}
		for _, gone := range []string{slog.TimeKey, slog.LevelKey, slog.MessageKey, "severity"} {
			if _, present := p[gone]; present {
				t.Errorf("entry %d payload still carries %q: %v", i, gone, p)
			}
		}
		if _, ok := p["message"].(string); !ok {
			t.Errorf("entry %d payload has no message: %v", i, p)
		}
	}
	p0 := cap.entries[0].Payload.(map[string]any)
	if p0["message"] != "panaudia-server: listening" || p0["space"] != "main" || p0["order"] != float64(3) {
		t.Errorf("entry 0 payload = %v", p0)
	}
	p1 := cap.entries[1].Payload.(map[string]any)
	udp, _ := p1["udp"].(map[string]any)
	if udp["recv_bytes"] != float64(212992) {
		t.Errorf("group not nested: %v", p1)
	}
	p2 := cap.entries[2].Payload.(map[string]any)
	if p2["entity"] != "e1" || p2["err"] != "bad packet" {
		t.Errorf("With attrs lost: %v", p2)
	}
}

// The level filter still applies in front of the sink.
func TestCloudSinkHonoursLevel(t *testing.T) {
	lg, cap, _ := cloudTestLogger(slog.LevelWarn)
	lg.Info("dropped")
	lg.Warn("kept")
	if len(cap.entries) != 1 || cap.entries[0].Severity != logging.Warning {
		t.Fatalf("entries = %+v", cap.entries)
	}
}

// A line that is not a JSON object is shipped as text, never lost.
func TestCloudSinkNonJSONLine(t *testing.T) {
	_, cap, sink := cloudTestLogger(slog.LevelInfo)
	if _, err := sink.Write([]byte("plain text\n")); err != nil {
		t.Fatal(err)
	}
	if len(cap.entries) != 1 || cap.entries[0].Payload != "plain text\n" {
		t.Fatalf("entries = %+v", cap.entries)
	}
}

// Delivery failures are counted for /stats and reported on the
// fallback writer.
func TestCloudSinkErrorsCounted(t *testing.T) {
	_, _, sink := cloudTestLogger(slog.LevelInfo)
	fallback := &bytes.Buffer{}
	sink.fallback = fallback
	sink.onError(errors.New("rpc error: PermissionDenied"))
	sink.onError(logging.ErrOverflow)
	ls := &logSink{name: "cloud-logging", errors: &sink.errors}
	if st := ls.stats(); st.Sink != "cloud-logging" || st.Errors != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if !strings.Contains(fallback.String(), "PermissionDenied") {
		t.Fatalf("fallback = %q", fallback.String())
	}
	var none *logSink
	if st := none.stats(); st.Sink != "stderr" || st.Errors != 0 {
		t.Fatalf("nil stats = %+v", st)
	}
}

// The stderr sink is unchanged: text and json handlers on the writer,
// no errors counter, a no-op close.
func TestNewLoggerStderr(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		cfg := defaultConfig()
		cfg.LogFormat = format
		buf := &bytes.Buffer{}
		lg, sink, err := newLogger(context.Background(), cfg, buf)
		if err != nil {
			t.Fatal(err)
		}
		lg.Info("hello", "k", "v")
		if sink.stats().Sink != "stderr" || !strings.Contains(buf.String(), "hello") {
			t.Fatalf("%s: sink %s, out %q", format, sink.stats().Sink, buf.String())
		}
		sink.close()
		if format == "json" && !strings.HasPrefix(buf.String(), "{") {
			t.Fatalf("json format not honoured: %q", buf.String())
		}
	}
}

// Without a project and without a metadata server the cloud sink
// fails the start, with the setting to fix it named.
func TestNewLoggerCloudFailsFastOffCloud(t *testing.T) {
	cfg := defaultConfig()
	cfg.LogSink = "cloud-logging"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := newLogger(ctx, cfg, &bytes.Buffer{})
	if err == nil {
		t.Skip("a metadata server answered: running on Google Cloud")
	}
	if !strings.Contains(err.Error(), "PANAUDIA_LOG_PROJECT") {
		t.Fatalf("err = %v, want the project setting named", err)
	}
}

func TestConfigLogSink(t *testing.T) {
	t.Setenv("PANAUDIA_ALLOW_UNTICKETED", "true")
	cases := []struct {
		sink, project string
		wantErr       bool
	}{
		{"", "", false},
		{"stderr", "", false},
		{"cloud-logging", "", false},
		{"Cloud-Logging", "lark-audio", false},
		{"journald", "", true},
	}
	for _, c := range cases {
		t.Setenv("PANAUDIA_LOG_SINK", c.sink)
		t.Setenv("PANAUDIA_LOG_PROJECT", c.project)
		cfg, err := loadConfig()
		if (err != nil) != c.wantErr {
			t.Fatalf("%q: err = %v, wantErr %v", c.sink, err, c.wantErr)
		}
		if err != nil {
			continue
		}
		want := strings.ToLower(c.sink)
		if want == "" {
			want = "stderr"
		}
		if cfg.LogSink != want || cfg.LogProject != c.project {
			t.Errorf("%q: LogSink %q LogProject %q", c.sink, cfg.LogSink, cfg.LogProject)
		}
		eff := cfg.effective()
		if eff["PANAUDIA_LOG_SINK"] != want || eff["PANAUDIA_LOG_PROJECT"] != c.project {
			t.Errorf("%q: effective %v", c.sink, eff)
		}
	}
}
