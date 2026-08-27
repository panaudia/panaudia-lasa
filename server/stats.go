package main

// The minimal stats/health surface (build plan S6): one snapshot struct
// aggregating the engine's render counters with each live entity's
// ingest diagnostics — jitter depth (Source.LatencySamples) and the
// depacketizer's loss/reorder evidence. Consumed by the acceptance
// harnesses and logged periodically by the binary; an HTTP surface can
// wrap the same snapshot when a deployment wants one.

import (
	"log/slog"
	"sort"
	"time"

	"github.com/panaudia/lasa/server"

	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/engine"
)

type serverStats struct {
	Engine   engine.ProcessStats `json:"engine"`
	Entities []entityStats       `json:"entities"`
	UDP      udpBuffers          `json:"udp"` // listener socket buffers as tuned at startup
}

type entityStats struct {
	ID             string                    `json:"id"`
	ClientID       string                    `json:"client_id"`
	LatencySamples int                       `json:"latency_samples"`
	DecodeErrors   uint64                    `json:"decode_errors"`
	EncodeErrors   uint64                    `json:"encode_errors"` // summed over the entity's live sinks
	Jitter         buffers.JitterBufferStats `json:"jitter"`
	Depacketizer   server.DepacketizerStats  `json:"depacketizer"`
}

func (a *app) stats() serverStats {
	return serverStats{Engine: a.mixer.Stats(), Entities: a.backend.entityStats(), UDP: a.udp}
}

// encodeErrors sums the encode-error counters of the entity's live
// sinks (binaural and/or ambi orders). Lock-free: one atomic load of
// the COW fan-out list.
func (rec *entityRec) encodeErrors() uint64 {
	var n uint64
	if sinks := rec.sinks.Load(); sinks != nil {
		for _, k := range *sinks {
			n += k.EncodeErrors()
		}
	}
	return n
}

func (b *backend) entityStats() []entityStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]entityStats, 0, len(b.entities))
	for id, rec := range b.entities {
		out = append(out, entityStats{
			ID:             id,
			ClientID:       rec.clientID,
			LatencySamples: rec.src.LatencySamples(),
			DecodeErrors:   rec.src.DecodeErrors(),
			EncodeErrors:   rec.encodeErrors(),
			Jitter:         rec.src.JitterStats(),
			Depacketizer:   rec.dep.Stats(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// statsLoop logs a compact health line every interval while entities are
// live. Started by main; runs for the process lifetime. At a fast
// cadence (PANAUDIA_STATS_SEC <= 15, the jitter-debugging mode) it also
// logs one detail line per entity: the ingest jitter snapshot (fill,
// live window, discontinuity counters) + the depacketizer breakdown.
func (a *app) statsLoop(interval time.Duration) {
	detail := interval <= 15*time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		s := a.stats()
		if len(s.Entities) == 0 {
			continue
		}
		per := s.Engine.PerTick()
		var delivered, lost, gaps, decodeErrors, encodeErrors uint64
		maxFill := 0
		for _, e := range s.Entities {
			delivered += e.Depacketizer.Delivered
			lost += e.Depacketizer.Lost + e.Depacketizer.Skipped
			gaps += e.Depacketizer.GapEvents
			decodeErrors += e.DecodeErrors
			encodeErrors += e.EncodeErrors
			if e.LatencySamples > maxFill {
				maxFill = e.LatencySamples
			}
		}
		slog.Info("stats",
			"entities", len(s.Entities), "ticks", s.Engine.Ticks,
			slog.Group("perTick", "prep", per.Prep, "in", per.In, "across", per.Across, "out", per.Out),
			"maxJitterFillSamples", maxFill,
			slog.Group("ingress", "delivered", delivered, "lost", lost, "gapEvents", gaps, "decodeErrors", decodeErrors),
			slog.Group("egress", "encodeErrors", encodeErrors))
		if !detail {
			continue
		}
		for _, e := range s.Entities {
			j := e.Jitter
			d := e.Depacketizer
			// v4 snapshot fields (48 kHz: 48 frames/ms). sp is the
			// servo setpoint, wl/wh the measured per-side widths,
			// rate the live splice rate, trim the macro-trim count.
			slog.Info("stats: entity", "entity", e.ID, "decodeErrors", e.DecodeErrors, "encodeErrors", e.EncodeErrors,
				slog.Group("jbuf",
					"fillMs", float64(j.FillFrames)/48.0, "setpointMs", float64(j.SetpointFrames)/48.0,
					"widthLowMs", float64(j.WidthLowFrames)/48.0, "widthHighMs", float64(j.WidthHighFrames)/48.0,
					"ratePerSec", j.RatePerSec, "underruns", j.Underruns, "overruns", j.Overruns,
					"laps", j.Laps, "trims", j.Trims, "inserted", j.SamplesInserted, "dropped", j.SamplesDropped,
					"frozen", j.Frozen),
				slog.Group("dep",
					"delivered", d.Delivered, "recovered", d.Recovered, "lost", d.Lost, "skipped", d.Skipped,
					"duplicates", d.Duplicates, "late", d.Late, "gapEvents", d.GapEvents, "gapHist", d.GapHist))
		}
	}
}
