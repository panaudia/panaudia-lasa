package jitterlab

import (
	"testing"
)

// goldenCheck holds per-scenario behavioural expectations. These are
// phase-2 sanity bounds (does the scenario provoke the documented
// behaviour class?), not the phase-4 baseline — deliberately loose.
type goldenCheck func(t *testing.T, res *Result)

func TestGoldenSuiteV3(t *testing.T) {
	g := DefaultGeometry()
	checks := map[string]goldenCheck{
		"calm": func(t *testing.T, res *Result) {
			expectZeroArtifacts(t, res, res.Summary.JoinTime+int64(2*g.SampleRate))
		},
		"browser-writer-clumps": func(t *testing.T, res *Result) {
			if res.Summary.Drops < 50 {
				t.Errorf("expected heavy drop-splicing from clumps, got %d", res.Summary.Drops)
			}
		},
		"bluetooth-reader": func(t *testing.T, res *Result) {
			if res.Summary.Snaps+res.Summary.Drops < 10 {
				t.Errorf("expected churn from reader clumps, got snaps=%d drops=%d",
					res.Summary.Snaps, res.Summary.Drops)
			}
		},
		"dac-drift+200": func(t *testing.T, res *Result) {
			if res.Summary.Drops < 1000 {
				t.Errorf("fast writer must force sustained drops, got %d", res.Summary.Drops)
			}
			if res.Summary.Inserts > res.Summary.Drops/10 {
				t.Errorf("drift must be one-sided: drops=%d inserts=%d",
					res.Summary.Drops, res.Summary.Inserts)
			}
		},
		"dac-drift-200": func(t *testing.T, res *Result) {
			// Phase-2 finding (2026-08-11): with W = R the fill lattice
			// steps over v3's insert band (width S=1ms < R=5ms), so
			// slow-writer drift produces periodic underrun CLICKS and no
			// splices at all — the rate reconciler is dead in this
			// geometry. Locked in here as v3 baseline behaviour; a v4
			// acceptance criterion is the inverse (splices, no clicks).
			if res.Summary.Inserts != 0 {
				t.Errorf("expected the quantization pathology (no inserts), got %d", res.Summary.Inserts)
			}
			if res.Summary.SilenceRuns < 10 {
				t.Errorf("slow drift must click periodically (~1 packet slip/25s), got %d runs",
					res.Summary.SilenceRuns)
			}
		},
		"ticker-catchup": func(t *testing.T, res *Result) {
			if res.Summary.Snaps < 1 {
				t.Errorf("large catch-up must trip the overrun snap, got %d snaps", res.Summary.Snaps)
			}
			if res.Summary.SilenceRuns < 1 {
				t.Errorf("the stall itself must starve the reader, got %d silence runs", res.Summary.SilenceRuns)
			}
		},
		"ticker-catchup-small": func(t *testing.T, res *Result) {
			// v3 baseline (see TickerCatchupSmall doc): the in-band
			// offset drains — one silence click plus a drop drip, no
			// snap, and no permanent time-domain latency change. v4's
			// bar: same recovery, fewer artifacts.
			if res.Summary.Snaps != 0 {
				t.Errorf("offset lands in-band, no snap expected, got %d", res.Summary.Snaps)
			}
			if res.Summary.Drops < 100 || res.Summary.Drops > 700 {
				t.Errorf("expected a bounded drop drip (100–700), got %d", res.Summary.Drops)
			}
			pre := res.Latency(res.Summary.JoinTime + int64(2*g.SampleRate))
			post := res.Latency(res.Duration - int64(10*g.SampleRate))
			gain := (post.MeanF - pre.MeanF) * post.FramesMs
			if gain > 1 || gain < -1 {
				t.Errorf("time-domain latency should fully recover (|Δ|≤1ms), got %+.2fms", gain)
			}
		},
		"outage-resume": func(t *testing.T, res *Result) {
			if res.Summary.LossGaps < 1 {
				t.Errorf("outage must surface as a loss gap, got %d", res.Summary.LossGaps)
			}
			if res.Summary.SilenceFrames < int64(2*g.SampleRate) {
				t.Errorf("a 3s outage must starve the reader, got %d silence frames", res.Summary.SilenceFrames)
			}
		},
		"loss-random": func(t *testing.T, res *Result) {
			if res.Summary.LossGaps < 100 {
				t.Errorf("2%% loss over 2min ≈ 480 lost packets; got %d loss gaps", res.Summary.LossGaps)
			}
		},
		"drift+clumps": func(t *testing.T, res *Result) {
			churn := res.Summary.Drops + res.Summary.Snaps + res.Summary.SilenceRuns
			if churn < 300 {
				t.Errorf("zero-headroom drift+clumps must churn continuously, got drops=%d snaps=%d runs=%d",
					res.Summary.Drops, res.Summary.Snaps, res.Summary.SilenceRuns)
			}
		},
		"encode-sweep-tail": func(t *testing.T, res *Result) {
			// One-sided lateness costs DROPS (the delayed packets bunch
			// with their on-time successors) — matching the ch15/16 field
			// signature, not the naive inserts prediction.
			if res.Summary.Drops < 50 {
				t.Errorf("one-sided lateness must cost drop splices (field signature), got %d",
					res.Summary.Drops)
			}
		},
		"onset-after-calm": func(t *testing.T, res *Result) {
			half := res.Duration / 2
			pre := res.ArtifactsAfter(ArtifactInsert, res.Summary.JoinTime+int64(2*g.SampleRate)) -
				res.ArtifactsAfter(ArtifactInsert, half)
			post := res.ArtifactsAfter(ArtifactInsert, half) + res.ArtifactsAfter(ArtifactSilence, half)
			if pre != 0 {
				t.Errorf("calm half must be artifact-free, got %d inserts", pre)
			}
			if post == 0 {
				t.Errorf("jitter onset after narrowing must cost corrections, got none")
			}
		},
	}

	for _, sc := range GoldenSuite(g, 7) {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			jb := newV3(g)
			res := Run(jb, sc)
			if res.Summary.JoinTime < 0 {
				t.Fatal("never produced audio")
			}
			crossCheck(t, res, jb, g)
			if chk, ok := checks[sc.Name]; ok {
				chk(t, res)
			}
			s := res.Summary
			t.Logf("%s: drops=%d ins=%d snaps=%d lossgaps=%d silRuns=%d silFrames=%d aliens=%d",
				sc.Name, s.Drops, s.Inserts, s.Snaps, s.LossGaps, s.SilenceRuns, s.SilenceFrames, s.AlienSamples)
		})
	}
}

// TestGoldenSuiteLegacyGeometry runs the suite under v3's 20/5 ms
// geometry — decoder/cross-check validation only; behavioural
// expectations are geometry-specific and belong to the phase-4 baseline.
func TestGoldenSuiteLegacyGeometry(t *testing.T) {
	g := LegacyGeometry()
	for _, sc := range GoldenSuite(g, 7) {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			jb := newV3(g)
			res := Run(jb, sc)
			if res.Summary.JoinTime < 0 {
				t.Fatal("never produced audio")
			}
			crossCheck(t, res, jb, g)
		})
	}
}

func expectZeroArtifacts(t *testing.T, res *Result, from int64) {
	t.Helper()
	for _, k := range []ArtifactKind{ArtifactDrop, ArtifactInsert, ArtifactSnap, ArtifactSilence} {
		if n := res.ArtifactsAfter(k, from); n != 0 {
			t.Errorf("%v after warm-up: %d, want 0", k, n)
		}
	}
}
