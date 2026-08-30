package main

// Sink cloud-logging: the server's own log stream goes to Google Cloud
// Logging over its API, touching neither stderr nor a disk. On a
// Compute Engine VM this needs no configuration: credentials come from
// the metadata server (application default credentials), the project
// is detected the same way, and the client labels every entry with the
// gce_instance resource. The service account still needs the
// logging.logWriter role; newCloudSink pings the API at startup so a
// missing role fails the start, not the first log line an hour later.
//
// The records are the JSON handler's, unchanged: cloudSink is the
// io.Writer the handler writes one line per record into, and each line
// becomes one entry with its time and level lifted into the entry and
// its msg renamed to message (the field Cloud Logging displays). Attrs
// and groups therefore render exactly as PANAUDIA_LOG_FORMAT=json
// would print them. The client batches entries in memory (a second or
// a thousand entries, whichever first, at most cloudBufferLimit bytes
// in flight); what it cannot deliver is counted in /stats as
// log.errors and reported on the fallback writer. What never reaches
// slog at all, a Go panic or a failure before the logger exists, still
// goes to stderr.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/logging"
)

// cloudLogID names the log: projects/<project>/logs/panaudia-server.
const cloudLogID = "panaudia-server"

// cloudBufferLimit bounds the client's in-memory backlog; beyond it
// entries are dropped (ErrOverflow, counted) rather than growing the
// heap while the API is unreachable.
const cloudBufferLimit = 8 << 20

// entryLogger is the slice of *logging.Logger the sink uses, so tests
// can capture entries without a client.
type entryLogger interface {
	Log(logging.Entry)
}

type cloudSink struct {
	lg       entryLogger
	fallback io.Writer
	closeFn  func()
	errors   atomic.Uint64
}

func newCloudSink(ctx context.Context, cfg appConfig, fallback io.Writer) (*cloudSink, error) {
	project := cfg.LogProject
	if project == "" {
		p, err := metadata.ProjectIDWithContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloud-logging: PANAUDIA_LOG_PROJECT unset and no metadata server: %w", err)
		}
		project = p
	}
	client, err := logging.NewClient(ctx, "projects/"+project)
	if err != nil {
		return nil, fmt.Errorf("cloud-logging: %w", err)
	}
	s := &cloudSink{fallback: fallback}
	client.OnError = s.onError
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("cloud-logging: project %s not writable (logging.logWriter role?): %w", project, err)
	}
	s.lg = client.Logger(cloudLogID, logging.BufferedByteLimit(cloudBufferLimit))
	s.closeFn = func() { _ = client.Close() } // Close flushes every logger first
	return s, nil
}

// cloudHandlerOptions adapts the JSON handler's top-level keys to what
// the sink lifts out of each record: the level becomes a Cloud Logging
// severity name, msg becomes message. Group members are left alone.
func cloudHandlerOptions(opts *slog.HandlerOptions) *slog.HandlerOptions {
	o := *opts
	o.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) > 0 {
			return a
		}
		switch a.Key {
		case slog.LevelKey:
			if l, ok := a.Value.Any().(slog.Level); ok {
				return slog.String("severity", cloudSeverity(l).String())
			}
		case slog.MessageKey:
			a.Key = "message"
		}
		return a
	}
	return &o
}

func cloudSeverity(l slog.Level) logging.Severity {
	switch {
	case l >= slog.LevelError:
		return logging.Error
	case l >= slog.LevelWarn:
		return logging.Warning
	case l >= slog.LevelInfo:
		return logging.Info
	default:
		return logging.Debug
	}
}

// Write receives one JSON record per call (the handler writes each
// record with a single Write) and hands it to the client as one entry.
// It never fails: a line that is not a JSON object (nothing the handler
// emits) is shipped as text rather than lost.
func (s *cloudSink) Write(p []byte) (int, error) {
	var payload map[string]any
	if err := json.Unmarshal(p, &payload); err != nil || payload == nil {
		s.lg.Log(logging.Entry{Payload: string(p)})
		return len(p), nil
	}
	e := logging.Entry{Payload: payload}
	if v, ok := payload["severity"].(string); ok {
		e.Severity = logging.ParseSeverity(v)
		delete(payload, "severity")
	}
	if v, ok := payload[slog.TimeKey].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			e.Timestamp = t
			delete(payload, slog.TimeKey)
		}
	}
	s.lg.Log(e)
	return len(p), nil
}

// onError is the client's failure callback (a batch the API refused,
// an overflow, an invalid entry); never concurrent, expected to be
// quick.
func (s *cloudSink) onError(err error) {
	s.errors.Add(1)
	fmt.Fprintf(s.fallback, "cloud-logging: %v\n", err)
}

func (s *cloudSink) close() {
	if s.closeFn != nil {
		s.closeFn()
	}
}
