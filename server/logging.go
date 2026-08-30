package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

// newLogger builds the process logger from the config: one handler,
// filtered at the configured level, on the configured sink. Sink
// "stderr" (the default) is text or JSON on w; sink "cloud-logging"
// (logging_cloud.go) ships the same JSON records to Google Cloud
// Logging over its API and uses w only to report delivery failures.
// main installs the logger as the slog default, which also routes the
// stdlib log package (and so any dependency still using log.Printf)
// into the same stream, and calls the returned sink's close on every
// exit path so buffered entries are flushed.
func newLogger(ctx context.Context, cfg appConfig, w io.Writer) (*slog.Logger, *logSink, error) {
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogSink == "cloud-logging" {
		cs, err := newCloudSink(ctx, cfg, w)
		if err != nil {
			return nil, nil, err
		}
		h := slog.NewJSONHandler(cs, cloudHandlerOptions(opts))
		return slog.New(h), &logSink{name: cfg.LogSink, errors: &cs.errors, close: cs.close}, nil
	}
	var h slog.Handler
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), &logSink{name: "stderr", close: func() {}}, nil
}

// logSink is what the rest of the process knows about where logs go:
// a name and a failure count for /stats, and the flush-and-close hook.
type logSink struct {
	name   string
	errors *atomic.Uint64 // nil when the sink cannot fail (stderr)
	close  func()
}

type logStats struct {
	Sink   string `json:"sink"`
	Errors uint64 `json:"errors"` // delivery failures reported by the sink (batches, not entries)
}

// stats is nil-safe: an app built without main (the tests) reports the
// stderr sink.
func (s *logSink) stats() logStats {
	if s == nil {
		return logStats{Sink: "stderr"}
	}
	st := logStats{Sink: s.name}
	if s.errors != nil {
		st.Errors = s.errors.Load()
	}
	return st
}
