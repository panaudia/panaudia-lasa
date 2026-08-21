package jitterlab

import (
	"testing"
	"time"
)

// --- codec ---

func TestRampRoundTrip(t *testing.T) {
	const nc = 2
	dst := make([]float32, 7*nc)
	// Straddle the wrap to exercise both encode and unwrap.
	start := RampWrap - 3
	fillRamp(dst, start, 7, nc)
	last := start - 1
	for f := int64(0); f < 7; f++ {
		for ch := 0; ch < nc; ch++ {
			kind, w := classifySample(dst[f*nc+int64(ch)])
			if kind != sampPlain {
				t.Fatalf("frame %d ch %d: kind %d, want plain", f, ch, kind)
			}
			if ch == 0 {
				abs := unwrapPos(last, w)
				if abs != start+f {
					t.Fatalf("frame %d: unwrapped %d, want %d", f, abs, start+f)
				}
				last = abs
			}
		}
	}
}

func TestClassifySample(t *testing.T) {
	cases := []struct {
		v    float32
		kind sampleKind
	}{
		{0, sampSilence},
		{1, sampPlain},
		{float32(RampWrap), sampPlain},
		{100.5, sampMarker},
		{float32(RampWrap) + 7, sampAlien},
		{-3, sampAlien},
		{0.25, sampAlien},
	}
	for _, c := range cases {
		k, _ := classifySample(c.v)
		if k != c.kind {
			t.Errorf("classify(%v) = %d, want %d", c.v, k, c.kind)
		}
	}
}

// --- decoder (synthetic v3-shaped blocks) ---

// plainBlock builds a block of n contiguous positions starting at pos.
func plainBlock(pos, n int64) []float32 {
	b := make([]float32, n)
	fillRamp(b, pos, n, 1)
	return b
}

func TestDecoderSpliceAndSnap(t *testing.T) {
	d := newDecoder(1)
	const n = 8

	// Read 1: normal, positions 100..107.
	d.feed(0, plainBlock(100, n))
	// Read 2: v3 drop shape — positions 108..114, then marker over
	// (115,116); the next read resumes at 117.
	blk := plainBlock(108, n)
	blk[n-1] = markerValue(115)
	d.feed(n, blk)
	// Read 3: resumes at 117 (net one frame dropped).
	d.feed(2*n, plainBlock(117, n))
	// Read 4: v3 insert shape — positions 125..131, marker previewing
	// (131,132); next read resumes at 132.
	blk = plainBlock(125, n)
	blk[n-1] = markerValue(131)
	d.feed(3*n, blk)
	// Read 5: resumes at 132 (net one frame inserted).
	d.feed(4*n, plainBlock(132, n))
	// Read 6: silence (underrun), two reads long.
	d.feed(5*n, make([]float32, n))
	d.feed(6*n, make([]float32, n))
	// Read 8: snap — resumes far ahead.
	d.feed(7*n, plainBlock(500, n))
	d.finish()

	if d.drops != 1 || d.inserts != 1 || d.snaps != 1 || d.silenceRuns != 1 {
		t.Fatalf("drops=%d inserts=%d snaps=%d silenceRuns=%d, want 1 each",
			d.drops, d.inserts, d.snaps, d.silenceRuns)
	}
	if d.alienSamples != 0 {
		t.Fatalf("aliens=%d, want 0", d.alienSamples)
	}
	// Snap delta: expected next was 140, got 500.
	for _, a := range d.artifacts {
		if a.Kind == ArtifactSnap && a.Delta != 500-140 {
			t.Errorf("snap delta = %d, want %d", a.Delta, 500-140)
		}
		if a.Kind == ArtifactSilence && a.Delta != 2*n {
			t.Errorf("silence run = %d frames, want %d", a.Delta, 2*n)
		}
	}
}

// --- v3 smoke tests ---

func newV3(g Geometry) *V3Buffer {
	frameDur := func(frames int64) time.Duration {
		return time.Duration(frames) * time.Second / time.Duration(g.SampleRate)
	}
	return NewV3Buffer(V3Config{
		SampleRate:      g.SampleRate,
		NumChannels:     g.NumChannels,
		WriterFrameSize: frameDur(g.WriterFrames),
		ReaderFrameSize: frameDur(g.ReaderFrames),
	})
}

// crossCheck asserts the black-box ledger agrees with the buffer's
// self-reported stats — the phase-1 money test: the harness measures
// from the output alone what the buffer says it did. Tolerance: a splice
// landing exactly on a loss gap averages positions the codec cannot pair
// (an alien sample), hiding that one splice and possibly desyncing one
// snap — so each count may differ by at most the alien count. Zero
// aliens (every scenario without loss) ⇒ exact equality.
func crossCheck(t *testing.T, res *Result, jb *V3Buffer, g Geometry) {
	t.Helper()
	st := jb.Snapshot()
	tol := res.Summary.AlienSamples
	absDiff := func(a, b int64) int64 {
		if a > b {
			return a - b
		}
		return b - a
	}
	if absDiff(res.Summary.Drops, st.SamplesDropped) > tol {
		t.Errorf("ledger drops %d vs buffer SamplesDropped %d (tol %d)", res.Summary.Drops, st.SamplesDropped, tol)
	}
	if absDiff(res.Summary.Inserts, st.SamplesInserted) > tol {
		t.Errorf("ledger inserts %d vs buffer SamplesInserted %d (tol %d)", res.Summary.Inserts, st.SamplesInserted, tol)
	}
	if absDiff(res.Summary.Snaps, st.Overruns+st.Laps) > tol {
		t.Errorf("ledger snaps %d vs buffer overruns+laps %d (tol %d)", res.Summary.Snaps, st.Overruns+st.Laps, tol)
	}
	// Every underrun read clears exactly R frames; aliens don't affect it.
	if res.Summary.SilenceFrames != st.Underruns*g.ReaderFrames {
		t.Errorf("ledger silence %d frames != buffer underruns %d × R %d",
			res.Summary.SilenceFrames, st.Underruns, g.ReaderFrames)
	}
}

func TestCalmV3(t *testing.T) {
	g := DefaultGeometry()
	dur := int64(60 * g.SampleRate) // 60 s
	sc := Calm(g, dur, g.MsToFrames(2.5))
	jb := newV3(g)
	res := Run(jb, sc)

	if res.Summary.JoinTime < 0 {
		t.Fatal("never produced audio")
	}
	// After warm-up: a calm network must produce zero artifacts.
	from := res.Summary.JoinTime + int64(2*g.SampleRate)
	for _, k := range []ArtifactKind{ArtifactDrop, ArtifactInsert, ArtifactSnap, ArtifactSilence} {
		if n := res.ArtifactsAfter(k, from); n != 0 {
			t.Errorf("%v after warm-up: %d, want 0", k, n)
		}
	}
	// And a flat latency trajectory (within one write frame).
	lat := res.Latency(from)
	if lat.Count == 0 {
		t.Fatal("no latency samples after warm-up")
	}
	if lat.MaxF-lat.MinF > g.WriterFrames {
		t.Errorf("latency spread %d frames > W=%d (min %d max %d)",
			lat.MaxF-lat.MinF, g.WriterFrames, lat.MinF, lat.MaxF)
	}
	if ms := lat.MeanF * lat.FramesMs; ms <= 0 || ms > 30 {
		t.Errorf("mean latency %.2f ms outside sane range (0, 30]", ms)
	}
	crossCheck(t, res, jb, g)
}

func TestIIDJitterV3(t *testing.T) {
	g := DefaultGeometry()
	dur := int64(180 * g.SampleRate) // 3 min
	sc := IIDJitter(g, dur, g.MsToFrames(5), g.MsToFrames(4), 1)
	jb := newV3(g)
	res := Run(jb, sc)

	if res.Summary.JoinTime < 0 {
		t.Fatal("never produced audio")
	}
	// Heavy one-sided jitter (σ=4 ms against L≈5 ms) must surface real
	// corrections — otherwise the harness isn't seeing anything.
	if res.Summary.Inserts+res.Summary.SilenceRuns == 0 {
		t.Error("expected inserts and/or underrun runs under heavy jitter, got none")
	}
	crossCheck(t, res, jb, g)
	t.Logf("iid-jitter: drops=%d inserts=%d snaps=%d silenceRuns=%d silenceFrames=%d joinMs=%.1f",
		res.Summary.Drops, res.Summary.Inserts, res.Summary.Snaps,
		res.Summary.SilenceRuns, res.Summary.SilenceFrames,
		float64(res.Summary.JoinTime)*1000/float64(g.SampleRate))
}

func TestDeterminism(t *testing.T) {
	g := DefaultGeometry()
	dur := int64(30 * g.SampleRate)
	run := func() *Result {
		sc := IIDJitter(g, dur, g.MsToFrames(5), g.MsToFrames(4), 42)
		return Run(newV3(g), sc)
	}
	a, b := run(), run()
	if a.Summary != b.Summary {
		t.Fatalf("summaries differ across identical runs:\n%+v\n%+v", a.Summary, b.Summary)
	}
	if len(a.Artifacts) != len(b.Artifacts) {
		t.Fatalf("artifact counts differ: %d vs %d", len(a.Artifacts), len(b.Artifacts))
	}
	for i := range a.Artifacts {
		if a.Artifacts[i] != b.Artifacts[i] {
			t.Fatalf("artifact %d differs: %+v vs %+v", i, a.Artifacts[i], b.Artifacts[i])
		}
	}
}
