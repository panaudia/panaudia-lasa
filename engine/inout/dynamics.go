package inout

import "math"

// StereoCompressorLimiter applies feed-forward compression and brick-wall
// limiting to interleaved stereo float32 audio. Stereo-linked peak detection
// ensures identical gain reduction on both channels, preserving the spatial
// stereo image.
type StereoCompressorLimiter struct {
	// Compressor parameters
	compThresholdDb float32
	compRatio       float32
	compKneeDb      float32
	compMakeupLin   float32

	// Compressor envelope coefficients
	compAttackCoeff  float32
	compReleaseCoeff float32

	// Limiter parameters
	limCeilingLin float32

	// Limiter envelope coefficients
	limAttackCoeff  float32
	limReleaseCoeff float32

	// Runtime state (per-sample, no allocations)
	compEnvelope float32
	limEnvelope  float32
}

// NewStereoCompressorLimiter creates a compressor/limiter with sensible
// defaults for multi-source spatial audio summing.
func NewStereoCompressorLimiter(sampleRate float32, frameSize int) *StereoCompressorLimiter {
	d := &StereoCompressorLimiter{}

	// Compressor defaults
	d.compThresholdDb = -18.0
	d.compRatio = 2.5
	d.compKneeDb = 6.0
	d.compMakeupLin = 1.0 // no makeup gain — never boost, only attenuate

	d.compAttackCoeff = coeffFromTime(10.0e-3, sampleRate)  // 10 ms
	d.compReleaseCoeff = coeffFromTime(150.0e-3, sampleRate) // 150 ms

	// Limiter defaults
	d.limCeilingLin = dbToLin(-0.5)

	d.limAttackCoeff = coeffFromTime(0.1e-3, sampleRate) // 0.1 ms
	d.limReleaseCoeff = coeffFromTime(50.0e-3, sampleRate) // 50 ms

	return d
}

// Process applies compression then limiting in-place to interleaved stereo
// samples (L0,R0,L1,R1,...). This is the hot path — zero allocations.
func (d *StereoCompressorLimiter) Process(buf []float32) {
	n := len(buf)
	for i := 0; i < n-1; i += 2 {
		l := buf[i]
		r := buf[i+1]

		// --- Stereo-linked peak detection ---
		peak := maxf32(absf32(l), absf32(r))

		// --- Compressor ---
		// Smooth the peak envelope
		if peak > d.compEnvelope {
			d.compEnvelope += d.compAttackCoeff * (peak - d.compEnvelope)
		} else {
			d.compEnvelope += d.compReleaseCoeff * (peak - d.compEnvelope)
		}

		compGainLin := d.computeCompressorGain(d.compEnvelope)

		// Apply compressor gain + makeup
		l *= compGainLin * d.compMakeupLin
		r *= compGainLin * d.compMakeupLin

		// --- Limiter ---
		postPeak := maxf32(absf32(l), absf32(r))

		if postPeak > d.limEnvelope {
			d.limEnvelope += d.limAttackCoeff * (postPeak - d.limEnvelope)
		} else {
			d.limEnvelope += d.limReleaseCoeff * (postPeak - d.limEnvelope)
		}

		if d.limEnvelope > d.limCeilingLin {
			limGain := d.limCeilingLin / d.limEnvelope
			l *= limGain
			r *= limGain
		}

		// --- Hard clip safety ---
		l = clampf32(l, -1.0, 1.0)
		r = clampf32(r, -1.0, 1.0)

		buf[i] = l
		buf[i+1] = r
	}
}

// computeCompressorGain returns the linear gain to apply, given the current
// envelope level. Uses soft-knee computation in the dB domain.
func (d *StereoCompressorLimiter) computeCompressorGain(envelope float32) float32 {
	if envelope < 1e-10 {
		return 1.0
	}

	envDb := linToDb(envelope)
	halfKnee := d.compKneeDb * 0.5
	kneeBottom := d.compThresholdDb - halfKnee
	kneeTop := d.compThresholdDb + halfKnee

	var gainReductionDb float32

	if envDb <= kneeBottom {
		// Below knee — no compression
		return 1.0
	} else if envDb >= kneeTop {
		// Above knee — full ratio
		gainReductionDb = (envDb - d.compThresholdDb) * (1.0 - 1.0/d.compRatio)
	} else {
		// In the knee — quadratic interpolation
		x := envDb - kneeBottom
		gainReductionDb = (1.0 - 1.0/d.compRatio) * x * x / (2.0 * d.compKneeDb)
	}

	return dbToLin(-gainReductionDb)
}

// coeffFromTime returns a one-pole smoothing coefficient for the given time
// constant (in seconds) and sample rate.
func coeffFromTime(timeSec float32, sampleRate float32) float32 {
	if timeSec <= 0 {
		return 1.0
	}
	return 1.0 - float32(math.Exp(float64(-1.0/(timeSec*sampleRate))))
}

// dbToLin converts decibels to linear amplitude.
func dbToLin(db float32) float32 {
	return float32(math.Pow(10.0, float64(db)/20.0))
}

// linToDb converts linear amplitude to decibels.
func linToDb(lin float32) float32 {
	return 20.0 * float32(math.Log10(float64(lin)))
}

func absf32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func maxf32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func clampf32(x, lo, hi float32) float32 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
