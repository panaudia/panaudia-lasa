package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a bytes.Buffer safe to read while the shell's
// goroutines are still logging into it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog routes the process logger into a buffer for one test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// logLines returns the records whose msg is the one given.
func logLines(buf *syncBuffer, msg string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// A session's story is told at info: one line per entity at join,
// one at departure with its ingest counters, both with the ids an
// operator filters on.
func TestLifecycleLogsJoinAndLeave(t *testing.T) {
	buf := captureLog(t)
	a := startTestApp(t, nil)
	c, err := dialTest(t, a, "c1", "e1", "e2")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "join of e1+e2", func() bool { return settled(a, "e1", "e2") })
	// The join line is written after the conn-map insert that settled
	// observes, so wait for the lines themselves rather than sampling
	// once (a loaded CI runner sat in that gap, 2026-09-03).
	waitFor(t, "join log lines", func() bool { return len(logLines(buf, "entity joined")) == 2 })
	joined := logLines(buf, "entity joined")
	if len(joined) != 2 {
		t.Fatalf("entity joined lines = %d, want 2:\n%s", len(joined), buf.String())
	}
	for _, rec := range joined {
		if rec["client"] != "c1" || rec["entity"] == nil || rec["name"] == nil {
			t.Errorf("join line missing ids: %v", rec)
		}
		for _, k := range []string{"quality", "redundancy", "dof", "entities"} {
			if _, ok := rec[k].(float64); !ok {
				t.Errorf("join line missing %s: %v", k, rec)
			}
		}
	}
	_ = c.Close()
	waitFor(t, "departure sweep", func() bool { return settled(a) })
	waitFor(t, "departure log lines", func() bool { return len(logLines(buf, "entity left")) == 2 })
	for _, rec := range logLines(buf, "entity left") {
		if rec["client"] != "c1" || rec["entity"] == nil {
			t.Errorf("leave line missing ids: %v", rec)
		}
		if _, ok := rec["duration"].(string); !ok {
			t.Errorf("leave line missing duration: %v", rec)
		}
		if _, ok := rec["depacketizer"].(map[string]any); !ok {
			t.Errorf("leave line missing depacketizer stats: %v", rec)
		}
	}
	if n := len(logLines(buf, "stats")); n != 0 {
		t.Logf("%d stats lines during the test (cadence-driven, fine)", n)
	}
}
