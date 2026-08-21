package inout

import (
	"math"
	"testing"
)

const testSampleRate = 48000.0
const testFrameSize = 240

func newTestDynamics() *StereoCompressorLimiter {
	return NewStereoCompressorLimiter(testSampleRate, testFrameSize)
}

// makeStereoTone generates interleaved stereo samples at the given linear amplitude.
func makeStereoTone(amplitude float32, samples int) []float32 {
	buf := make([]float32, samples*2)
	for i := 0; i < samples; i++ {
		// Simple 1 kHz sine at the given amplitude
		val := amplitude * float32(math.Sin(2.0*math.Pi*1000.0*float64(i)/testSampleRate))
		buf[i*2] = val
		buf[i*2+1] = val
	}
	return buf
}

func peakLevel(buf []float32) float32 {
	var peak float32
	for _, s := range buf {
		if absf32(s) > peak {
			peak = absf32(s)
		}
	}
	return peak
}

func TestBelowThresholdPassesThrough(t *testing.T) {
	d := newTestDynamics()
	// -30 dBFS is well below the -18 dBFS threshold
	amp := dbToLin(-30.0)
	buf := makeStereoTone(amp, testFrameSize)
	original := make([]float32, len(buf))
	copy(original, buf)

	// Run several frames to let envelope settle
	for i := 0; i < 20; i++ {
		copy(buf, original)
		d.Process(buf)
	}

	// After settling, output should be close to input (no makeup gain, no compression)
	outPeak := peakLevel(buf)
	ratio := outPeak / amp
	if ratio < 0.85 || ratio > 1.15 {
		t.Errorf("below-threshold signal changed too much: peak=%f, expected~%f, ratio=%f", outPeak, amp, ratio)
	}
}

func TestAboveThresholdReducesGain(t *testing.T) {
	d := newTestDynamics()
	// -6 dBFS is well above the -18 dBFS threshold
	amp := dbToLin(-6.0)
	buf := makeStereoTone(amp, testFrameSize)

	// Process many frames for the envelope to converge
	for i := 0; i < 50; i++ {
		buf = makeStereoTone(amp, testFrameSize)
		d.Process(buf)
	}

	outPeak := peakLevel(buf)
	// With 2.5:1 compression above -18 dBFS and no makeup,
	// the output should be lower than input
	if outPeak >= amp {
		t.Errorf("compressor did not reduce gain: outPeak=%f >= input=%f", outPeak, amp)
	}
}

func TestSoftKneeContinuity(t *testing.T) {
	// Verify that gain changes smoothly across the knee region.
	// The knee spans -21 to -15 dBFS.
	d1 := newTestDynamics()
	d2 := newTestDynamics()
	d3 := newTestDynamics()

	levels := []float32{-22.0, -18.0, -14.0} // below knee, at threshold, above knee
	dynamics := []*StereoCompressorLimiter{d1, d2, d3}
	var gains [3]float32

	for idx, lvl := range levels {
		amp := dbToLin(lvl)
		for i := 0; i < 100; i++ {
			buf := makeStereoTone(amp, testFrameSize)
			dynamics[idx].Process(buf)
		}
		buf := makeStereoTone(amp, testFrameSize)
		dynamics[idx].Process(buf)
		gains[idx] = peakLevel(buf) / amp
	}

	// Gains should be monotonically decreasing (or equal) as level increases
	if gains[1] > gains[0]*1.05 {
		t.Errorf("knee not monotonic: gain at -18=%f > gain at -22=%f", gains[1], gains[0])
	}
	if gains[2] > gains[1]*1.05 {
		t.Errorf("knee not monotonic: gain at -14=%f > gain at -18=%f", gains[2], gains[1])
	}
}

func TestLimiterEnforcesCeiling(t *testing.T) {
	d := newTestDynamics()
	// Way above ceiling — 0 dBFS
	amp := float32(1.0)

	// Process many frames to let limiter fully engage
	var buf []float32
	for i := 0; i < 100; i++ {
		buf = makeStereoTone(amp, testFrameSize)
		d.Process(buf)
	}

	outPeak := peakLevel(buf)
	ceiling := dbToLin(-0.5)
	// Allow a tiny margin for envelope transients
	if outPeak > ceiling*1.05 {
		t.Errorf("limiter failed: outPeak=%f > ceiling=%f (with 5%% margin)", outPeak, ceiling)
	}
}

func TestHardClipSafety(t *testing.T) {
	d := newTestDynamics()
	// Extreme input that could exceed 1.0 after makeup
	buf := make([]float32, testFrameSize*2)
	for i := range buf {
		buf[i] = 5.0 // way beyond clipping
	}
	d.Process(buf)

	for i, s := range buf {
		if s > 1.0 || s < -1.0 {
			t.Fatalf("hard clip failed at sample %d: value=%f", i, s)
		}
	}
}

func TestStereoLinking(t *testing.T) {
	d := newTestDynamics()
	// Feed asymmetric stereo — L is loud, R is quiet
	buf := make([]float32, testFrameSize*2)
	for i := 0; i < testFrameSize; i++ {
		val := float32(math.Sin(2.0 * math.Pi * 1000.0 * float64(i) / testSampleRate))
		buf[i*2] = val * 0.8   // L: -1.9 dBFS
		buf[i*2+1] = val * 0.1 // R: -20 dBFS
	}

	// Run many frames to settle
	for f := 0; f < 50; f++ {
		for i := 0; i < testFrameSize; i++ {
			val := float32(math.Sin(2.0 * math.Pi * 1000.0 * float64(i) / testSampleRate))
			buf[i*2] = val * 0.8
			buf[i*2+1] = val * 0.1
		}
		d.Process(buf)
	}

	// Compute gain applied to each channel by looking at mid-frame samples
	// (avoid envelope transient at frame start)
	midIdx := testFrameSize / 2
	lGain := absf32(buf[midIdx*2]) / (absf32(float32(math.Sin(2.0*math.Pi*1000.0*float64(midIdx)/testSampleRate))) * 0.8)
	rGain := absf32(buf[midIdx*2+1]) / (absf32(float32(math.Sin(2.0*math.Pi*1000.0*float64(midIdx)/testSampleRate))) * 0.1)

	// Both channels should have approximately the same gain (stereo linked)
	ratio := lGain / rGain
	if ratio < 0.85 || ratio > 1.15 {
		t.Errorf("stereo linking broken: L gain=%f, R gain=%f, ratio=%f", lGain, rGain, ratio)
	}
}

func TestNeverAddsGain(t *testing.T) {
	// At every input level, output peak must be <= input peak.
	// This ensures distant/quiet sources are never boosted.
	levels := []float32{-40, -30, -20, -18, -12, -6, -3, 0}
	for _, lvlDb := range levels {
		d := newTestDynamics()
		amp := dbToLin(lvlDb)

		// Let envelope settle
		var buf []float32
		for i := 0; i < 100; i++ {
			buf = makeStereoTone(amp, testFrameSize)
			d.Process(buf)
		}

		outPeak := peakLevel(buf)
		if outPeak > amp*1.01 { // 1% tolerance for float rounding
			t.Errorf("gain was added at %f dBFS: input peak=%f, output peak=%f", lvlDb, amp, outPeak)
		}
	}
}

func BenchmarkDynamicsProcess(b *testing.B) {
	d := newTestDynamics()
	buf := makeStereoTone(0.5, testFrameSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Process(buf)
	}
}
