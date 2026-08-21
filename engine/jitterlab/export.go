package jitterlab

// export.go — canonical trace export for port validation
// (plan/jitter-v4-ts-port.md phase 1). A trace fixture carries a
// scenario's full event schedule, the buffer config used, and the
// artifact ledger + summary the Go reference produced. A port replays
// the events against its own core and asserts LEDGER EQUALITY — the
// same decision at every read of every scenario — which is the
// lockstep mechanism between implementations (harness doc, phase 5).
//
// Event kinds: 0 = write, 1 = read, 2 = lost (position accounting
// only), 3 = concealment write (zero content — the FEC reassembler's
// give-up frame). Artifact kinds are the ArtifactKind values verbatim.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// TraceFixture is the on-disk schema (JSON).
type TraceFixture struct {
	Name     string       `json:"name"`
	Geometry TraceGeo     `json:"geometry"`
	Config   TraceConfig  `json:"config"`
	Duration int64        `json:"duration"`
	Events   [][4]int64   `json:"events"` // [time, kind, frames, pos]
	Ledger   [][4]int64   `json:"ledger"` // [kind, time, pos, delta]
	Summary  TraceSummary `json:"summary"`
}

type TraceGeo struct {
	SampleRate   int   `json:"sampleRate"`
	NumChannels  int   `json:"numChannels"`
	WriterFrames int64 `json:"writerFrames"`
	ReaderFrames int64 `json:"readerFrames"`
}

type TraceConfig struct {
	FloorFrames    int64   `json:"floorFrames"`
	Robustness     float64 `json:"robustness"`
	CapacityFrames int64   `json:"capacityFrames"`
}

type TraceSummary struct {
	Drops           int64 `json:"drops"`
	Inserts         int64 `json:"inserts"`
	Snaps           int64 `json:"snaps"`
	LossGaps        int64 `json:"lossGaps"`
	SilenceRuns     int64 `json:"silenceRuns"`
	SilenceFrames   int64 `json:"silenceFrames"`
	ConcealedFrames int64 `json:"concealedFrames"`
	AlienSamples    int64 `json:"alienSamples"`
	JoinTime        int64 `json:"joinTime"`
}

// ExportTrace runs the scenario against a fresh v4 buffer built from
// cfg and writes the fixture. Deterministic: same scenario + config ⇒
// byte-identical file.
func ExportTrace(dir string, sc Scenario, cfg TraceConfig) error {
	b := NewV4Buffer(V4Config{
		SampleRate:     sc.Geometry.SampleRate,
		NumChannels:    sc.Geometry.NumChannels,
		WriterFrames:   sc.Geometry.WriterFrames,
		ReaderFrames:   sc.Geometry.ReaderFrames,
		FloorFrames:    cfg.FloorFrames,
		Robustness:     cfg.Robustness,
		CapacityFrames: cfg.CapacityFrames,
	})
	res := Run(b, sc)

	fx := TraceFixture{
		Name: sc.Name,
		Geometry: TraceGeo{
			SampleRate:   sc.Geometry.SampleRate,
			NumChannels:  sc.Geometry.NumChannels,
			WriterFrames: sc.Geometry.WriterFrames,
			ReaderFrames: sc.Geometry.ReaderFrames,
		},
		Config:   cfg,
		Duration: sc.Duration,
	}
	for _, e := range sc.Events {
		kind := int64(0)
		switch {
		case e.Kind == KindWrite && e.Conceal:
			kind = 3
		case e.Kind == KindWrite:
			kind = 0
		case e.Kind == KindRead:
			kind = 1
		case e.Kind == KindLost:
			kind = 2
		}
		fx.Events = append(fx.Events, [4]int64{e.Time, kind, e.Frames, e.Pos})
	}
	for _, a := range res.Artifacts {
		fx.Ledger = append(fx.Ledger, [4]int64{int64(a.Kind), a.Time, a.Pos, a.Delta})
	}
	s := res.Summary
	fx.Summary = TraceSummary{
		Drops: s.Drops, Inserts: s.Inserts, Snaps: s.Snaps,
		LossGaps: s.LossGaps, SilenceRuns: s.SilenceRuns,
		SilenceFrames: s.SilenceFrames, ConcealedFrames: s.ConcealedFrames,
		AlienSamples: s.AlienSamples, JoinTime: s.JoinTime,
	}

	data, err := json.Marshal(fx)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sc.Name+".json"), data, 0o644)
}
