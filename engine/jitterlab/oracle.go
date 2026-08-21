package jitterlab

// oracle.go — the phase-4 oracle and scorecard (plan/jitter-v4-harness.md,
// "oracle and scorecard"). From the schedule alone (no buffer), compute
// the minimal latency an ideal buffer would have needed, then score any
// run as regret against it.
//
// Definitions:
//
//   - The reader's demand accrues at each read event at the writer's true
//     rate (ground-truth ppm from the scenario) — the oracle is
//     rate-matched, so deficits reflect delay variation only; rate
//     reconciliation is priced separately (its physical minimum is the
//     drift rate itself, in frames/s).
//   - Lost frames count as delivered at their send time (best case):
//     genuinely lost audio is concealment's problem, not latency's.
//   - deficit_j = demand_j − delivered(t_j) at read j. The CONSTANT
//     oracle is D* = max_j deficit_j: an ideal fixed-latency buffer
//     holding post-read fill D* − deficit_j just never starves. The
//     WINDOWED oracle re-computes the max over a trailing window — the
//     trajectory an instantly-adapting buffer could follow; aspirational
//     by construction.
//   - All latencies are POST-READ FILL means (the DUT's recorded
//     FillFrames uses the same convention), so regret = DUT − oracle is
//     apples-to-apples. Intra-packet phase (< W) is common-mode.
//
// Reader clumps cost the oracle nothing (the data had arrived; absorbing
// a reader stall needs capacity, not latency) — so a buffer that churns
// on them shows up as pure regret, which is the point.

import "fmt"

// OracleResult is the offline oracle for one scenario.
type OracleResult struct {
	DFrames          int64   // constant-oracle worst-case deficit
	MeanFillConst    float64 // post-warmup mean post-read fill at D*
	MeanFillAdaptive float64 // trailing-window oracle mean fill (aspirational)
}

// ComputeOracle runs the deficit analysis over the schedule. warmup is in
// master-clock frames; reads before it are covered by D* but excluded
// from the means. window is the trailing windowed-oracle length in reads
// (0 = 400).
func ComputeOracle(sc Scenario, warmup int64, window int64) OracleResult {
	if window == 0 {
		window = 400
	}
	demandRate := float64(sc.Geometry.ReaderFrames) * (1 + sc.WriterPPM*1e-6)

	var delivered int64
	var demand float64
	type defRec struct {
		time int64
		def  float64
	}
	defs := make([]defRec, 0, len(sc.Events)/2)
	for _, e := range sc.Events {
		switch e.Kind {
		case KindWrite:
			delivered += e.Frames
		case KindLost:
			delivered += e.Frames // best case: see header
		case KindRead:
			demand += demandRate
			defs = append(defs, defRec{e.Time, demand - float64(delivered)})
		}
	}

	var res OracleResult
	maxDef := 0.0
	for _, d := range defs {
		if d.def > maxDef {
			maxDef = d.def
		}
	}
	res.DFrames = int64(maxDef + 0.5)

	// Post-warmup means: constant fill = D* − def; adaptive fill =
	// (trailing-window max def) − def, via a monotonic deque of indices.
	type idxDef struct {
		idx int
		def float64
	}
	var dq []idxDef
	var sumC, sumA float64
	var n int64
	for i, d := range defs {
		for len(dq) > 0 && dq[len(dq)-1].def <= d.def {
			dq = dq[:len(dq)-1]
		}
		dq = append(dq, idxDef{i, d.def})
		for len(dq) > 0 && dq[0].idx <= i-int(window) {
			dq = dq[1:]
		}
		if d.time < warmup {
			continue
		}
		sumC += maxDef - d.def
		sumA += dq[0].def - d.def
		n++
	}
	if n > 0 {
		res.MeanFillConst = sumC / float64(n)
		res.MeanFillAdaptive = sumA / float64(n)
	}
	return res
}

// Scorecard is one run scored against the oracle.
type Scorecard struct {
	Scenario string

	OracleMs         float64 // constant-oracle mean latency
	AdaptiveOracleMs float64 // windowed-oracle mean latency (aspirational)
	DutMs            float64 // DUT post-warmup mean latency (post-read fill)
	RegretMs         float64 // DutMs − OracleMs

	// Post-warmup artifact densities, per minute of virtual time.
	SplicesPerMin float64 // drops + inserts
	SnapsPerMin   float64
	SilenceMsPerMin float64

	// Final-third densities — steady state after adaptation.
	SteadySplicesPerMin float64

	// Time of the last artifact as a fraction of the run (1.0 = still
	// churning at the end; small = settled early).
	LastArtifactFrac float64
}

// BuildScorecard scores a Result against its scenario's oracle.
func BuildScorecard(res *Result, sc Scenario) Scorecard {
	g := sc.Geometry
	sr := float64(g.SampleRate)
	warmup := res.Summary.JoinTime + int64(2*g.SampleRate)
	orc := ComputeOracle(sc, warmup, 0)

	sc4 := Scorecard{Scenario: sc.Name}
	sc4.OracleMs = orc.MeanFillConst / sr * 1000
	sc4.AdaptiveOracleMs = orc.MeanFillAdaptive / sr * 1000

	var fillSum, fillN float64
	for i, t := range res.ReadTimes {
		if t < warmup || res.FillFrames[i] < 0 {
			continue
		}
		fillSum += float64(res.FillFrames[i])
		fillN++
	}
	if fillN > 0 {
		sc4.DutMs = fillSum / fillN / sr * 1000
	}
	sc4.RegretMs = sc4.DutMs - sc4.OracleMs

	postMin := float64(res.Duration-warmup) / sr / 60
	thirdStart := res.Duration - res.Duration/3
	thirdMin := float64(res.Duration/3) / sr / 60
	var splices, snaps, silence, steadySplices int64
	var lastArtifact int64
	for _, a := range res.Artifacts {
		if a.Time > lastArtifact {
			lastArtifact = a.Time
		}
		if a.Time < warmup {
			continue
		}
		switch a.Kind {
		case ArtifactDrop, ArtifactInsert:
			splices++
			if a.Time >= thirdStart {
				steadySplices++
			}
		case ArtifactSnap:
			snaps++
		case ArtifactSilence:
			silence += a.Delta
		}
	}
	if postMin > 0 {
		sc4.SplicesPerMin = float64(splices) / postMin
		sc4.SnapsPerMin = float64(snaps) / postMin
		sc4.SilenceMsPerMin = float64(silence) / sr * 1000 / postMin
	}
	if thirdMin > 0 {
		sc4.SteadySplicesPerMin = float64(steadySplices) / thirdMin
	}
	if res.Duration > 0 {
		sc4.LastArtifactFrac = float64(lastArtifact) / float64(res.Duration)
	}
	return sc4
}

// Row formats the scorecard as one fixed-width report line.
func (s Scorecard) Row() string {
	return fmt.Sprintf("%-22s oracle=%6.2fms adapt=%6.2fms dut=%6.2fms regret=%+7.2fms splices/min=%7.1f steady=%7.1f snaps/min=%5.1f silence=%6.0fms/min last=%.2f",
		s.Scenario, s.OracleMs, s.AdaptiveOracleMs, s.DutMs, s.RegretMs,
		s.SplicesPerMin, s.SteadySplicesPerMin, s.SnapsPerMin, s.SilenceMsPerMin, s.LastArtifactFrac)
}
