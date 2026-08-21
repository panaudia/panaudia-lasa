package main

// The minimal stats/health surface (build plan S6): one snapshot struct
// aggregating the engine's render counters with each live entity's
// ingest diagnostics — jitter depth (Source.LatencySamples) and the
// depacketizer's loss/reorder evidence. Consumed by the acceptance
// harnesses and logged periodically by the binary; an HTTP surface can
// wrap the same snapshot when a deployment wants one.

import (
	"log"
	"sort"
	"time"

	"github.com/panaudia/lasa/server"

	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/engine"
)

type serverStats struct {
	Engine   engine.ProcessStats `json:"engine"`
	Entities []entityStats       `json:"entities"`
}

type entityStats struct {
	ID             string                    `json:"id"`
	ClientID       string                    `json:"client_id"`
	LatencySamples int                       `json:"latency_samples"`
	Jitter         buffers.JitterBufferStats `json:"jitter"`
	Depacketizer   server.DepacketizerStats  `json:"depacketizer"`
}

func (a *app) stats() serverStats {
	return serverStats{Engine: a.mixer.Stats(), Entities: a.backend.entityStats()}
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
		var delivered, lost, gaps uint64
		maxFill := 0
		for _, e := range s.Entities {
			delivered += e.Depacketizer.Delivered
			lost += e.Depacketizer.Lost + e.Depacketizer.Skipped
			gaps += e.Depacketizer.GapEvents
			if e.LatencySamples > maxFill {
				maxFill = e.LatencySamples
			}
		}
		log.Printf("stats: entities=%d ticks=%d per-tick{prep=%s in=%s across=%s out=%s} max-jitter-fill=%dsmp ingress{delivered=%d lost=%d gap-events=%d}",
			len(s.Entities), s.Engine.Ticks, per.Prep, per.In, per.Across, per.Out, maxFill, delivered, lost, gaps)
		if !detail {
			continue
		}
		for _, e := range s.Entities {
			j := e.Jitter
			d := e.Depacketizer
			// v4 snapshot fields (48 kHz: 48 frames/ms). sp is the
			// servo setpoint, wl/wh the measured per-side widths,
			// rate the live splice rate, trim the macro-trim count.
			log.Printf("stats[%s]: jbuf{fill=%.1fms sp=%.1fms wl=%.1f wh=%.1f rate=%+.1f/s und=%d ovr=%d lap=%d trim=%d ins=%d drop=%d frozen=%v} dep{del=%d rec=%d lost=%d skip=%d dup=%d late=%d gaps=%d hist=%v}",
				e.ID, float64(j.FillFrames)/48.0, float64(j.SetpointFrames)/48.0,
				float64(j.WidthLowFrames)/48.0, float64(j.WidthHighFrames)/48.0,
				j.RatePerSec, j.Underruns, j.Overruns, j.Laps, j.Trims,
				j.SamplesInserted, j.SamplesDropped, j.Frozen,
				d.Delivered, d.Recovered, d.Lost, d.Skipped, d.Duplicates, d.Late, d.GapEvents, d.GapHist)
		}
	}
}
