package jitterlab

// sensorlab_test.go — the phase-3 discrimination matrix: does each
// candidate sensor separate drift (slope) from burst (width) from offset
// (step), and what does each sensing domain miss? Ground truth comes
// from the generator parameters (plan/jitter-v4-harness.md, "sensor
// lab"). Tolerances are lab bounds, not baseline numbers.

import "testing"

// --- estimate helpers ---

func lastEst(ests []Estimate) Estimate {
	if len(ests) == 0 {
		return Estimate{}
	}
	return ests[len(ests)-1]
}

// estAtOrBefore returns the last estimate whose Tick ≤ tick.
func estAtOrBefore(ests []Estimate, tick int64) Estimate {
	out := Estimate{}
	for _, e := range ests {
		if e.Tick > tick {
			break
		}
		out = e
	}
	return out
}

// maxWidthLow returns the largest late-side width over estimates with
// Tick in [from, to).
func maxWidthLow(ests []Estimate, from, to int64) int64 {
	var m int64
	for _, e := range ests {
		if e.Tick >= from && e.Tick < to && e.WidthLow > m {
			m = e.WidthLow
		}
	}
	return m
}

func maxWidthHigh(ests []Estimate, from, to int64) int64 {
	var m int64
	for _, e := range ests {
		if e.Tick >= from && e.Tick < to && e.WidthHigh > m {
			m = e.WidthHigh
		}
	}
	return m
}

// refSlopePPM is the burst-immune (but lattice-quantized) drift view:
// the slope of the reference level across the whole run. tickToFrames
// converts the estimate clock to frames (R for fill sensors, 1 for the
// timestamp sensor whose Tick is already stream frames). Sign matches
// DriftPPM (+ = fast writer).
func refSlopePPM(ests []Estimate, tickToFrames int64, negate bool) float64 {
	if len(ests) < 2 {
		return 0
	}
	a, b := ests[0], ests[len(ests)-1]
	span := float64((b.Tick - a.Tick) * tickToFrames)
	if span == 0 {
		return 0
	}
	s := float64(b.Ref-a.Ref) / span * 1e6
	if negate {
		return -s
	}
	return s
}

func inRange(t *testing.T, name string, v, lo, hi float64) {
	t.Helper()
	if v < lo || v > hi {
		t.Errorf("%s = %.1f, want in [%.1f, %.1f]", name, v, lo, hi)
	}
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// --- passive matrix ---

func TestSensorLabPassive(t *testing.T) {
	g := DefaultGeometry()
	W := g.WriterFrames
	R := g.ReaderFrames
	ms := g.MsToFrames
	const seed = 11

	run := func(sc Scenario) (env, ts []Estimate) {
		e := NewEnvelopeSensor(EnvelopeSensorConfig{ReaderFrames: R})
		x := NewTimestampSensor(400 * R / W)
		PassiveTrace(sc, e, x)
		return e.Estimates(), x.Estimates()
	}
	logEsts := func(name string, env, ts []Estimate) {
		t.Logf("%s: env{ppm=%.1f refSlope=%.1f wLow=%d wHigh=%d} ts{ppm=%.1f wLow=%d}",
			name, lastEst(env).DriftPPM, refSlopePPM(env, R, false),
			maxWidthLow(env, 0, 1<<62), maxWidthHigh(env, 0, 1<<62),
			lastEst(ts).DriftPPM, maxWidthLow(ts, 0, 1<<62))
	}

	t.Run("calm", func(t *testing.T) {
		env, ts := run(Calm(g, secs(g, 60), ms(2.5)))
		logEsts("calm", env, ts)
		inRange(t, "env ppm", lastEst(env).DriftPPM, -3, 3)
		inRange(t, "ts ppm", lastEst(ts).DriftPPM, -3, 3)
		if w := maxWidthLow(env, 5000, 1<<62); w > 2*W {
			t.Errorf("calm env width %d > 2W", w)
		}
		if w := maxWidthLow(ts, 0, 1<<62); w > W/4 {
			t.Errorf("calm ts width %d, want ~0 (constant delay)", w)
		}
	})

	// Drift scoring uses the env sensor's Ref slope (burst-immune,
	// lattice-quantized, needs minutes) and the ts sensor's DriftPPM
	// (frame-resolution, needs seconds). The env mean-based DriftPPM is
	// packet-quantized in this geometry — logged as the finding it is,
	// never asserted (see Estimate.DriftPPM).
	for _, ppm := range []float64{21, -21, 200, -200} {
		sc := DACDrift(g, ppm)
		t.Run(sc.Name, func(t *testing.T) {
			env, ts := run(sc)
			logEsts(sc.Name, env, ts)
			lo, hi := ppm-0.35*absF(ppm)-4, ppm+0.35*absF(ppm)+4
			inRange(t, "env refSlope ppm", refSlopePPM(env, R, false), lo, hi)
			inRange(t, "ts ppm", lastEst(ts).DriftPPM, lo, hi)
			// Drift must NOT read as width (no inflation).
			if w := maxWidthLow(ts, 0, 1<<62); w > W {
				t.Errorf("drift read as ts width: %d", w)
			}
		})
	}

	t.Run("worker-gc", func(t *testing.T) {
		sc := WorkerGC(g, seed)
		env, ts := run(sc)
		logEsts("worker-gc", env, ts)
		// The headline: min/max-filter drift views stay clean under
		// bursts; clumps register as width, not slope.
		inRange(t, "ts ppm (min-filter, burst-immune)", lastEst(ts).DriftPPM, -5, 5)
		inRange(t, "env refSlope (max-filter, burst-immune)", refSlopePPM(env, R, false), -30, 30)
		if w := maxWidthLow(env, 0, 1<<62); w < ms(10) {
			t.Errorf("clumps must register as env width ≥10ms, got %d frames", w)
		}
		if w := maxWidthLow(ts, 0, 1<<62); w < ms(10) {
			t.Errorf("clumps must register as ts width ≥10ms, got %d frames", w)
		}
		// The mean-based fine estimator is expected to be burst-noisy;
		// record it (a finding, not a failure).
		t.Logf("worker-gc mean-based env ppm (burst-polluted): %.1f", lastEst(env).DriftPPM)
	})

	t.Run("bluetooth-reader", func(t *testing.T) {
		sc := BluetoothReader(g, seed)
		env, ts := run(sc)
		logEsts("bluetooth-reader", env, ts)
		// Reader clumps register on the HIGH side of the read-clock
		// signal — the tick clock stalls with the reader, so deliveries
		// accumulate against it (the correct side for width allocation).
		// The network view is clean: that separation is what timestamps
		// buy.
		if w := maxWidthHigh(env, 0, 1<<62); w < ms(20) {
			t.Errorf("reader clumps must register high-side in the read-clock signal, got %d", w)
		}
		if w := maxWidthLow(ts, 0, 1<<62); w > ms(2) {
			t.Errorf("network view should be clean under reader clumps, got %d", w)
		}
	})

	t.Run("encode-sweep-tail", func(t *testing.T) {
		sc := EncodeSweepTail(g, seed)
		env, ts := run(sc)
		logEsts("encode-sweep-tail", env, ts)
		inRange(t, "ts ppm", lastEst(ts).DriftPPM, -5, 5)
		if w := maxWidthLow(ts, 0, 1<<62); w < ms(4) {
			t.Errorf("one-sided lateness must register as ts width ≥4ms, got %d", w)
		}
	})

	t.Run("ticker-catchup", func(t *testing.T) {
		sc := TickerCatchup(g, 200)
		env, ts := run(sc)
		logEsts("ticker-catchup", env, ts)
		stallTick := sc.Duration / 2 / R
		pre := estAtOrBefore(env, stallTick-400)
		post := lastEst(env)
		if d := post.Ref - pre.Ref; d > W+R/4 || d < -(W+R/4) {
			t.Errorf("full catch-up must leave the env reference unchanged, Δ=%d", d)
		}
		inRange(t, "env ppm after recovery", post.DriftPPM, -10, 10)
		inRange(t, "ts ppm after recovery", lastEst(ts).DriftPPM, -5, 5)
	})

	t.Run("device-stall", func(t *testing.T) {
		sc := DeviceStall(g)
		env, ts := run(sc)
		logEsts("device-stall", env, ts)
		stallTick := sc.Duration / 2 / R
		pre := estAtOrBefore(env, stallTick-400)
		post := lastEst(env)
		if d := post.Ref - pre.Ref; d > W+R/4 || d < -(W+R/4) {
			t.Errorf("freeze+re-anchor must excise the loss step, Δ=%d", d)
		}
		// dev = t − pos cancels lost time against lost positions.
		tsPre := estAtOrBefore(ts, sc.Duration/2-ms(50))
		if d := lastEst(ts).Ref - tsPre.Ref; d > R/4 || d < -R/4 {
			t.Errorf("timestamp reference should pass through loss unchanged, Δ=%d", d)
		}
	})

	t.Run("outage-resume", func(t *testing.T) {
		sc := OutageResume(g)
		env, ts := run(sc)
		logEsts("outage-resume", env, ts)
		outTick := sc.Duration / 3 / R
		pre := estAtOrBefore(env, outTick-400)
		post := lastEst(env)
		if d := post.Ref - pre.Ref; d > W+R/4 || d < -(W+R/4) {
			t.Errorf("outage must teach the env sensor nothing, Δref=%d", d)
		}
		inRange(t, "env ppm after outage", post.DriftPPM, -10, 10)
		tsPre := estAtOrBefore(ts, sc.Duration/3-ms(50))
		if d := lastEst(ts).Ref - tsPre.Ref; d > R/4 || d < -R/4 {
			t.Errorf("timestamp reference should pass through outage unchanged, Δ=%d", d)
		}
	})
}

// --- attached matrix ---

// TestSensorLabAttached measures what live-buffer attachment does to the
// candidates: raw fill (wp − rp) is polluted by the buffer's own
// corrections; the virtual signal is not; and the v3 correction-counting
// gate is re-scored as the baseline it is.
func TestSensorLabAttached(t *testing.T) {
	g := DefaultGeometry()
	R := g.ReaderFrames
	ms := g.MsToFrames
	const seed = 11

	t.Run("drift-actuation-pollution", func(t *testing.T) {
		sc := DACDrift(g, 200)
		raw := NewEnvelopeSensor(EnvelopeSensorConfig{ReaderFrames: R, UseRawFill: true})
		virt := NewEnvelopeSensor(EnvelopeSensorConfig{ReaderFrames: R})
		RunProbed(newV3(g), sc, raw, virt)
		rawPPM := refSlopePPM(raw.Estimates(), R, false)
		virtPPM := refSlopePPM(virt.Estimates(), R, false)
		t.Logf("attached +200ppm: raw-fill refSlope=%.1f virtual refSlope=%.1f", rawPPM, virtPPM)
		// The buffer's drop corrections eat the drift out of raw fill —
		// a raw-fill sensor without actuation subtraction cannot see it.
		inRange(t, "raw-fill ppm (polluted toward 0)", rawPPM, -40, 40)
		inRange(t, "virtual ppm (clean)", virtPPM, 160, 240)
	})

	t.Run("reader-clump-sign", func(t *testing.T) {
		sc := BluetoothReader(g, seed)
		raw := NewEnvelopeSensor(EnvelopeSensorConfig{ReaderFrames: R, UseRawFill: true})
		virt := NewEnvelopeSensor(EnvelopeSensorConfig{ReaderFrames: R})
		RunProbed(newV3(g), sc, raw, virt)
		rawHigh := maxWidthHigh(raw.Estimates(), 0, 1<<62)
		virtHigh := maxWidthHigh(virt.Estimates(), 0, 1<<62)
		t.Logf("attached bluetooth: raw widthHigh=%d virt widthHigh=%d", rawHigh, virtHigh)
		// Both fill-domain views report reader clumps on the HIGH side —
		// the read-count clock stalls with the reader, so deliveries
		// accumulate against it. The hypothesized sign discrepancy
		// between them does not exist (lab finding); the domains agree
		// on the side that needs headroom.
		if rawHigh < ms(10) {
			t.Errorf("raw fill must see reader clumps as high-side width, got %d", rawHigh)
		}
		if virtHigh < ms(10) {
			t.Errorf("virtual signal must see reader clumps as high-side width, got %d", virtHigh)
		}
	})

	t.Run("v3-gate-baseline", func(t *testing.T) {
		sc := WorkerGC(g, seed)
		res := Run(newV3(g), sc)
		// Re-score v3's widen gate from the ledger: windows of 400 reads,
		// gate = min(drops, inserts) ≥ 5.
		winFrames := 400 * R
		nWin := sc.Duration / winFrames
		fired := 0
		for w := int64(0); w < nWin; w++ {
			var d, i int64
			for _, a := range res.Artifacts {
				if a.Time >= w*winFrames && a.Time < (w+1)*winFrames {
					switch a.Kind {
					case ArtifactDrop:
						d++
					case ArtifactInsert:
						i++
					}
				}
			}
			if min(d, i) >= 5 {
				fired++
			}
		}
		t.Logf("v3 gate on worker-gc: fired %d/%d windows, drops=%d", fired, nWin, res.Summary.Drops)
		if res.Summary.Drops < 1000 {
			t.Errorf("scenario should provoke heavy drops, got %d", res.Summary.Drops)
		}
		if float64(fired) > 0.05*float64(nWin) {
			t.Errorf("v3 gate should stay blind to one-sided bursts (the documented failure), fired %d/%d", fired, nWin)
		}
	})
}
