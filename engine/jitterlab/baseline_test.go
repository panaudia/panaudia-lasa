package jitterlab

// baseline_test.go — phase 4: oracle sanity and the v3 baseline report.
// TestBaselineReportV3 logs the full scorecard table; its numbers are
// transcribed into plan/jitter-v3-baseline.md, whose quarantined-failure
// list is v4's acceptance criteria.

import "testing"

func TestOracleSanity(t *testing.T) {
	g := DefaultGeometry()

	// Calm: the ideal buffer needs about one packet in flight; nothing
	// more.
	sc := Calm(g, secs(g, 60), g.MsToFrames(2.5))
	orc := ComputeOracle(sc, secs(g, 5), 0)
	if orc.DFrames < g.ReaderFrames || orc.DFrames > 3*g.WriterFrames {
		t.Errorf("calm oracle D* = %d frames, want ~1–3 packets", orc.DFrames)
	}
	if orc.MeanFillAdaptive > orc.MeanFillConst+1 {
		t.Errorf("adaptive oracle (%f) must not exceed constant (%f)",
			orc.MeanFillAdaptive, orc.MeanFillConst)
	}

	// Rate matching: pure drift must cost the oracle (almost) nothing —
	// deficits are delay variation only.
	scD := DACDrift(g, 200)
	orcD := ComputeOracle(scD, secs(g, 5), 0)
	if orcD.DFrames > 3*g.WriterFrames {
		t.Errorf("rate-matched oracle should not pay for drift, D* = %d frames", orcD.DFrames)
	}

	// Reader clumps need capacity, not latency: oracle ≈ calm.
	scB := BluetoothReader(g, 11)
	orcB := ComputeOracle(scB, secs(g, 5), 0)
	if orcB.DFrames > 4*g.WriterFrames {
		t.Errorf("reader clumps should cost the oracle ~nothing, D* = %d frames", orcB.DFrames)
	}

	// A catch-up stall is exactly a D*-sized event: the constant oracle
	// must pay for it.
	scT := TickerCatchup(g, 200)
	orcT := ComputeOracle(scT, secs(g, 5), 0)
	if lo := g.MsToFrames(150); orcT.DFrames < lo {
		t.Errorf("200ms stall must dominate the constant oracle, D* = %d frames", orcT.DFrames)
	}
}

// TestBaselineReportV3 produces the phase-4 baseline table. Run with -v
// to see it; the committed copy lives in plan/jitter-v3-baseline.md.
func TestBaselineReportV3(t *testing.T) {
	g := DefaultGeometry()
	for _, sc := range GoldenSuite(g, 7) {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			res := Run(newV3(g), sc)
			if res.Summary.JoinTime < 0 {
				t.Fatal("never produced audio")
			}
			card := BuildScorecard(res, sc)
			t.Log(card.Row())
		})
	}
}
