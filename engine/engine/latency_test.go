package engine

// The chirp latency harness: measures ACTUAL audio latency through the
// whole engine — WritePCM ingest → jitter buffer → spatial render →
// binaural encode → Opus — by playing a chirp through a real
// source/sink pair and cross-correlating the decoded output against
// the input. Deterministic (the feed interleaves packet writes with
// Process ticks; no wall-clock anywhere) and network-free.
//
// What the measured number contains: jitter-buffer fill (the dominant,
// adaptive term — reported separately via Source.LatencySamples so the
// residual is visible), tick quantization (0–240 samples), fixed DSP
// group delay (HRTF/convolver), and the Opus encode+decode algorithmic
// delay a real client also pays. What it deliberately excludes:
// network, client capture/playback — those belong to a server-level
// harness.
//
// The assertions are a tripwire (a regression that doubles latency
// must fail loudly), not a spec: the precise numbers are logged.

import (
	"math"
	"testing"

	"github.com/panaudia/panaudia-lasa/engine/inout"
)

func TestAudioLatencyChirp(t *testing.T) {
	if testing.Short() {
		t.Skip("latency harness is slow")
	}

	m, err := New(Config{Order: 3, MaxEntities: 4, ReverbPreset: ReverbNone, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// Source dead ahead of an unrotated listener at the origin: median
	// plane, so the per-ear ITDs are equal and add no lateral offset to
	// the correlation peak.
	src, err := m.AddSource("speaker", SourceConfig{InitialPose: Pose{X: 2}})
	if err != nil {
		t.Fatal(err)
	}
	w := &collectingWriter{}
	if _, err := m.AddSink("listener", w); err != nil {
		t.Fatal(err)
	}

	// A linear chirp (100 Hz → 6 kHz over 2 s): survives Opus's lossy
	// coding and gives a sharp, unambiguous correlation peak.
	const packetSamples = FrameSize // 5 ms packets, the default writer frame (LASA §5.1)
	const packets = 400
	chirp := make([]float32, packets*packetSamples)
	const f0, f1 = 100.0, 6000.0
	T := float64(len(chirp)) / SampleRate
	for i := range chirp {
		tt := float64(i) / SampleRate
		phase := 2 * math.Pi * (f0*tt + (f1-f0)/(2*T)*tt*tt)
		chirp[i] = float32(0.5 * math.Sin(phase))
	}

	// Feed at real cadence, deterministically: one 5 ms packet per
	// 5 ms tick. The jitter buffer sees exactly the interleaving it
	// would live on, minus network jitter.
	for p := 0; p < packets; p++ {
		src.WritePCM(uint64(p+1), nil, chirp[p*packetSamples:(p+1)*packetSamples])
		m.Process()
	}
	fill := src.LatencySamples() // steady-state jitter term

	// Decode the sink's Opus stream (stereo → summed mono).
	dec := inout.NewOpusInputDecoder(2)
	out := make([]float32, 0, packets*4*FrameSize)
	for _, f := range w.frames {
		out = append(out, dec.Decode(f)...)
	}
	if len(out) < 80000 {
		t.Fatalf("too little output audio: %d samples", len(out))
	}

	// Cross-correlate a mid-signal window (skipping the warm-start)
	// against the output over a generous lag range. |peak| so an
	// HRTF-induced polarity flip cannot hide the alignment.
	const refStart, refLen, maxLag = 24000, 48000, 6000
	best, bestLag := 0.0, -1
	for lag := 0; lag < maxLag; lag++ {
		var acc float64
		for i := 0; i < refLen; i++ {
			oi := refStart + lag + i
			if oi >= len(out) {
				break
			}
			acc += float64(chirp[refStart+i]) * float64(out[oi])
		}
		if a := math.Abs(acc); a > best {
			best, bestLag = a, lag
		}
	}
	if bestLag < 0 {
		t.Fatal("no correlation peak found")
	}

	ms := func(samples int) float64 { return float64(samples) * 1000.0 / SampleRate }
	residual := bestLag - fill
	t.Logf("audio latency: %d samples (%.1f ms) = jitter fill %d (%.1f ms) + residual %d (%.1f ms — tick alignment, DSP group delay, opus codec)",
		bestLag, ms(bestLag), fill, ms(fill), residual, ms(residual))

	// Tripwire bounds: generous, not a spec.
	if bestLag < 240 || bestLag > 4800 {
		t.Errorf("measured latency %d samples (%.1f ms) outside the sane range 5–100 ms", bestLag, ms(bestLag))
	}
	if residual < 0 {
		t.Errorf("residual negative (%d samples): measured latency below the jitter fill — measurement broken?", residual)
	}
}
