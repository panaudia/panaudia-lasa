// Package jitterlab is the deterministic jitter-buffer test harness
// (plan/jitter-v4-harness.md). It runs a buffer under test against
// synthetic network scenarios in virtual time — a single-threaded
// discrete-event simulation, no goroutines, no wall-clock — and measures
// it entirely from the outside via the self-describing ramp signal.
// Test-only: never imported by production code.
package jitterlab

import "sort"

// DUT is the surface a buffer under test must expose. It matches the
// production JitterBuffer; the harness stays version-agnostic by needing
// nothing else. (SPSC concurrency is exercised separately under -race;
// here both sides run interleaved on one goroutine by design.)
type DUT interface {
	Write(src []float32)
	Read(dst []float32) bool
}

// fillReporter is optionally implemented by a DUT (the production buffer
// implements it) to record a fill trajectory. Diagnostics only — never
// part of scoring.
type fillReporter interface {
	GetBehind() int // interleaved floats behind, i.e. fillFrames * nc
}

// writeRec maps a stream-position range to its arrival time.
type writeRec struct {
	startPos int64 // first stream frame position of this write
	time     int64 // master-clock arrival time
}

// Summary is the per-run rollup.
type Summary struct {
	Writes, Reads int64
	ProducedReads int64 // reads that returned audio
	SilentReads   int64 // reads that returned silence (incl. join)

	JoinSilenceFrames int64 // silence before first audio (startup, not artifact)
	JoinTime          int64 // master time of the first produced read; -1 if none

	Drops, Inserts, Snaps int64
	LossGaps              int64 // stream jumps fully explained by upstream loss
	SilenceRuns           int64 // underrun runs after first audio
	SilenceFrames         int64 // total frames in those runs
	ConcealedFrames       int64 // zero-content frames delivered by upstream concealment (FEC give-up)
	AlienSamples          int64
}

// Result is everything measured from one run.
type Result struct {
	Scenario string
	Geometry Geometry
	Duration int64 // scenario duration, master-clock frames
	Summary  Summary

	Artifacts []Artifact

	// Per-read series, indexed by read number. LatencyFrames is the
	// write→play latency of the read's first frame (-1 for silent reads).
	// FillFrames is the post-read fill (-1 if the DUT doesn't report).
	ReadTimes     []int64
	LatencyFrames []int64
	FillFrames    []int64
}

// Run executes the scenario against the DUT and returns the measurements.
func Run(dut DUT, sc Scenario) *Result { return runScenario(dut, sc, nil) }

// RunProbed is Run with sensors attached: each sensor receives every
// write (time, pos, frames) and a per-read Obs assembled from the live
// buffer (attached mode — Obs.Fill is real).
func RunProbed(dut DUT, sc Scenario, sensors ...Sensor) *Result {
	return runScenario(dut, sc, sensors)
}

// PassiveTrace drives sensors over a scenario with no buffer at all —
// the uncontrolled trace of the harness doc's sensor lab. Obs.Fill is -1;
// the virtual signal (delivered − R·tick) is the only fill-domain view.
func PassiveTrace(sc Scenario, sensors ...Sensor) {
	var delivered, tick int64
	for _, e := range sc.Events {
		switch e.Kind {
		case KindWrite:
			for _, s := range sensors {
				s.OnWrite(e.Time, e.Pos, e.Frames)
			}
			delivered += e.Frames
		case KindRead:
			o := Obs{Tick: tick, Time: e.Time, Delivered: delivered, Fill: -1}
			for _, s := range sensors {
				s.OnRead(o)
			}
			tick++
		}
	}
}

func runScenario(dut DUT, sc Scenario, sensors []Sensor) *Result {
	g := sc.Geometry
	nc := g.NumChannels

	res := &Result{Scenario: sc.Name, Geometry: g, Duration: sc.Duration}
	res.Summary.JoinTime = -1

	dec := newDecoder(nc)
	fr, hasFill := dut.(fillReporter)

	// Scratch buffers sized to the largest event of each kind.
	var maxW, maxR int64
	for _, e := range sc.Events {
		if e.Kind == KindWrite && e.Frames > maxW {
			maxW = e.Frames
		}
		if e.Kind == KindRead && e.Frames > maxR {
			maxR = e.Frames
		}
	}
	wbuf := make([]float32, maxW*int64(nc))
	rbuf := make([]float32, maxR*int64(nc))

	writes := make([]writeRec, 0, len(sc.Events)/2+1)
	type gap struct{ pos, frames int64 }
	var gaps []gap      // KindLost ranges: stream positions never written
	var concealed []gap // Conceal-write ranges: positions delivered as zeros

	var delivered, tick int64
	for _, e := range sc.Events {
		switch e.Kind {
		case KindWrite:
			src := wbuf[:e.Frames*int64(nc)]
			if e.Conceal {
				// Upstream concealment: zero content for a position the
				// reassembler gave up on. Recorded so the decoder's
				// silence run and position jump over the range are
				// attributed upstream, not to the buffer. (Starvation
				// silence over UNWRITTEN ranges deliberately stays
				// ArtifactSilence — plain loss keeps its v3-baseline
				// accounting.)
				clear(src)
				concealed = append(concealed, gap{pos: e.Pos, frames: e.Frames})
				res.Summary.ConcealedFrames += e.Frames
			} else {
				fillRamp(src, e.Pos, e.Frames, nc)
			}
			dut.Write(src)
			writes = append(writes, writeRec{startPos: e.Pos, time: e.Time})
			res.Summary.Writes++
			delivered += e.Frames
			for _, s := range sensors {
				s.OnWrite(e.Time, e.Pos, e.Frames)
			}

		case KindLost:
			gaps = append(gaps, gap{pos: e.Pos, frames: e.Frames})

		case KindRead:
			// Sensors sample fill at read entry, BEFORE the buffer acts:
			// v3's snap fires inside Read, so post-read fill has already
			// had the evidence corrected away (found by the reader-clump
			// sign experiment).
			fillPre := int64(-1)
			if hasFill {
				fillPre = int64(fr.GetBehind() / nc)
			}
			dst := rbuf[:e.Frames*int64(nc)]
			produced := dut.Read(dst)
			firstPos := dec.feed(e.Time, dst)
			res.Summary.Reads++
			if produced {
				res.Summary.ProducedReads++
				if res.Summary.JoinTime < 0 {
					res.Summary.JoinTime = e.Time
				}
			} else {
				res.Summary.SilentReads++
			}

			lat := int64(-1)
			if firstPos >= 0 {
				lat = e.Time - writeTimeOf(writes, firstPos)
			}
			fill := int64(-1)
			if hasFill {
				fill = int64(fr.GetBehind() / nc)
			}
			res.ReadTimes = append(res.ReadTimes, e.Time)
			res.LatencyFrames = append(res.LatencyFrames, lat)
			res.FillFrames = append(res.FillFrames, fill)
			for _, s := range sensors {
				s.OnRead(Obs{Tick: tick, Time: e.Time, Delivered: delivered, Fill: fillPre})
			}
			tick++
		}
	}

	dec.finish()
	res.Artifacts = dec.artifacts

	// Reclassify decoder artifacts that upstream loss/concealment fully
	// explains: snaps spanning unwritten or concealed ranges, and
	// silence runs that are exactly delivered concealment zeros.
	// Concealed entries can arrive out of position order (they land at
	// their give-up times), so sort first.
	sort.Slice(concealed, func(i, j int) bool { return concealed[i].pos < concealed[j].pos })
	rangeIn := func(list []gap, lo, hi int64) int64 {
		var n int64
		for _, gp := range list {
			if gp.pos >= hi {
				break
			}
			if gp.pos >= lo {
				n += gp.frames
			}
		}
		return n
	}
	for i, a := range res.Artifacts {
		if a.Kind == ArtifactSnap && a.Delta > 0 &&
			rangeIn(gaps, a.Pos-a.Delta, a.Pos)+rangeIn(concealed, a.Pos-a.Delta, a.Pos) == a.Delta {
			res.Artifacts[i].Kind = ArtifactLossGap
		}
		// A silence run that is exactly a concealed range (its zeros
		// played back) is upstream concealment, not buffer starvation.
		// The decoder reports the run at the last position BEFORE it.
		if a.Kind == ArtifactSilence && rangeIn(concealed, a.Pos+1, a.Pos+1+a.Delta) == a.Delta {
			res.Artifacts[i].Kind = ArtifactLossGap
		}
	}

	s := &res.Summary
	s.JoinSilenceFrames = dec.joinSilenceFrames
	s.AlienSamples = dec.alienSamples
	for _, a := range res.Artifacts {
		switch a.Kind {
		case ArtifactDrop:
			s.Drops++
		case ArtifactInsert:
			s.Inserts++
		case ArtifactSnap:
			s.Snaps++
		case ArtifactLossGap:
			s.LossGaps++
		case ArtifactSilence:
			s.SilenceRuns++
			s.SilenceFrames += a.Delta
		}
	}
	return res
}

// writeTimeOf returns the arrival time of the write containing stream
// position pos (binary search over the append-only write index).
func writeTimeOf(writes []writeRec, pos int64) int64 {
	// First record with startPos > pos, then step back.
	i := sort.Search(len(writes), func(i int) bool { return writes[i].startPos > pos })
	if i == 0 {
		return 0
	}
	return writes[i-1].time
}

// LatencyStats summarizes the latency series over reads at or after the
// given master time (use to exclude warm-up). Silent reads are skipped.
type LatencyStats struct {
	Count    int64
	MinF     int64
	MaxF     int64
	MeanF    float64
	FramesMs float64 // conversion factor: ms per frame, for convenience
}

// Latency computes LatencyStats from a Result for reads at/after fromTime.
func (r *Result) Latency(fromTime int64) LatencyStats {
	st := LatencyStats{MinF: int64(1) << 62, FramesMs: 1000.0 / float64(r.Geometry.SampleRate)}
	var sum int64
	for i, t := range r.ReadTimes {
		if t < fromTime || r.LatencyFrames[i] < 0 {
			continue
		}
		l := r.LatencyFrames[i]
		st.Count++
		sum += l
		if l < st.MinF {
			st.MinF = l
		}
		if l > st.MaxF {
			st.MaxF = l
		}
	}
	if st.Count > 0 {
		st.MeanF = float64(sum) / float64(st.Count)
	} else {
		st.MinF = 0
	}
	return st
}

// ArtifactsAfter counts artifacts of the given kind at or after fromTime.
func (r *Result) ArtifactsAfter(kind ArtifactKind, fromTime int64) int64 {
	var n int64
	for _, a := range r.Artifacts {
		if a.Kind == kind && a.Time >= fromTime {
			n++
		}
	}
	return n
}
