package main

import (
	"io"
	"log/slog"
	"os"
)

// newLogger builds the process logger from the config: one handler,
// stderr, text or JSON, filtered at the configured level. main installs
// it as the slog default, which also routes the stdlib log package (and
// so any dependency still using log.Printf) into the same stream.
func newLogger(cfg appConfig, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}
