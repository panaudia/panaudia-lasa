package ambisonic

// M4 verification, encoder level (plan milestone M4): the interaural lag
// between the two buses' packed content must match the Woodworth model —
// the delays are reinserted here and corrected to the measured ITD by the
// ear-aligned decode filters downstream (full-chain check lives in
// core/binaural).

import (
	"math"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// Golden values shared with the offline tool (tests/test_woodworth.py):
// a = 0.0875 m, c = 343 m/s. The two implementations must evaluate the
// same closed form — this is the cross-language half of the
// alignment-consistency invariant.
func TestWoodworthGoldenValues(t *testing.T) {
	setTestDelayModel(t)
	enc := NewEncoder(uuid.New(), false, 1.0, 2.0, m1Config(), 0)

	fs := float64(common.SAMPLE_RATE)
	cases := []struct {
		latSine float64
		wantS   float64
	}{
		{1.0, 6.558139e-4},   // az 90: (a/c)(pi/2 + 1)
		{-1.0, -6.558139e-4}, // az 270
		{0.5, 2.611229e-4},   // az 30: (a/c)(pi/6 + 1/2)
		{0.0, 0.0},           // median plane
	}
	for _, c := range cases {
		got := float64(enc.woodworthITDSamples(float32(c.latSine)))
		if math.Abs(got-c.wantS*fs) > 1e-3 {
			t.Errorf("latSine %.2f: %f samples, want %f", c.latSine, got, c.wantS*fs)
		}
	}
}

// A source hard left (az 90) must come out of the two buses with an
// interaural lag equal to the Woodworth ITD within ±1 sample; a frontal
// source with zero lag.
func TestBilateralBusITDMatchesWoodworth(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})

	for _, tc := range []struct {
		name    string
		pos     common.Position
		latSine float32
	}{
		{"left (az 90)", common.Position{X: 0.5, Y: 0.8, Z: 0.5}, 1.0},
		{"frontal", common.Position{X: 0.8, Y: 0.5, Z: 0.5}, 0.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 1)
			source.SetPosition(tc.pos)

			mixer, mixerR, reverbMixer := newTestMixers(cfg)
			rng := rand.New(rand.NewSource(9))
			frameSize := cfg.FrameSize
			bus := listener.BusSamples()

			var wL, wR []float32
			for frame := 0; frame < 8; frame++ {
				for j := range source.Input {
					source.Input[j] = rng.Float32()*2 - 1
				}
				source.PushInputRing()
				listener.EncodePeers([]*Encoder{source}, mixer, mixerR, reverbMixer)
				// W channel of each bus carries the (weighted, delayed) source.
				wL = append(wL, listener.Output[:frameSize]...)
				wR = append(wR, listener.Output[bus:bus+frameSize]...)
			}
			// Skip the fade-in frame.
			wL, wR = wL[frameSize:], wR[frameSize:]

			want := float64(listener.woodworthITDSamples(tc.latSine))
			lag := xcorrLag(wL, wR, 48)
			if math.Abs(lag-want) > 1.0 {
				t.Fatalf("bus interaural lag %.2f samples, want %.2f (Woodworth)", lag, want)
			}
		})
	}
}

// xcorrLag returns the lag (in samples, parabolic sub-sample refinement)
// maximizing correlation of a against b: positive = a leads b. The core
// argmax is the shared common.XCorrArgmax estimator.
func xcorrLag(a, b []float32, maxLag int) float64 {
	bestLag, scores := common.XCorrArgmax(a, b, -maxLag, maxLag, maxLag, len(a)-maxLag)
	// Parabolic refinement around the integer peak.
	if bestLag > -maxLag && bestLag < maxLag {
		y0, y1, y2 := scores[bestLag-1], scores[bestLag], scores[bestLag+1]
		den := y0 - 2*y1 + y2
		if den != 0 {
			return float64(bestLag) + 0.5*(y0-y2)/den
		}
	}
	return float64(bestLag)
}
