package ambisonic

// M6 verification (plan milestone M6): near-field ILD growth matches the
// spherical-head model (goldens from panaudia-hrtf's Duda-Martens code),
// the 2 m gate region is transparent, and a fast approach produces no
// zipper noise (coefficient interpolation + state continuity).

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// Golden DC-gain values from the offline model (sphere_dc_limit, a=0.0875,
// theta 0/180): the cross-language contract for the table's LF behaviour.
var nfcDCGoldens = []struct {
	distM          float64
	dcLdB, dcRdB   float64
}{
	{0.25, 5.33, -4.10},
	{0.50, 2.44, -2.15},
	{1.00, 1.18, -1.11},
}

func cascadeDCdB(c *[nfcNumSections][5]float32) float64 {
	g := 1.0
	for s := 0; s < nfcNumSections; s++ {
		g *= float64(c[s][0]+c[s][1]+c[s][2]) / float64(1+c[s][3]+c[s][4])
	}
	return 20 * math.Log10(g)
}

func TestNFCTableDCGoldens(t *testing.T) {
	prev := common.BilateralEarOffsetM
	common.BilateralEarOffsetM = 0.0875
	defer func() { common.BilateralEarOffsetM = prev }()

	var cL, cR [nfcNumSections][5]float32
	for _, g := range nfcDCGoldens {
		if !nfcCoefficients(g.distM, 1.0, &cL) || !nfcCoefficients(g.distM, -1.0, &cR) {
			t.Fatalf("d=%.2f inside the gate must be active", g.distM)
		}
		// The gate is fully open below 1.8 m; table fit + bilinear ≤ ~0.4 dB.
		if d := math.Abs(cascadeDCdB(&cL) - g.dcLdB); d > 0.5 {
			t.Errorf("d=%.2f near ear DC %.2f dB, model %.2f (Δ%.2f)", g.distM, cascadeDCdB(&cL), g.dcLdB, d)
		}
		if d := math.Abs(cascadeDCdB(&cR) - g.dcRdB); d > 0.5 {
			t.Errorf("d=%.2f far ear DC %.2f dB, model %.2f (Δ%.2f)", g.distM, cascadeDCdB(&cR), g.dcRdB, d)
		}
	}

	// Gate edge: just inside 2 m the blended coefficients are ~identity
	// (click-free entry with zeroed state); outside, inactive.
	if nfcCoefficients(2.0, 1.0, &cL) || nfcCoefficients(2.4, 1.0, &cL) {
		t.Error("gate must be off at >= 2 m")
	}
	if !nfcCoefficients(1.999, 1.0, &cL) {
		t.Fatal("gate must be on just inside 2 m")
	}
	for s := 0; s < nfcNumSections; s++ {
		for k := 0; k < 5; k++ {
			if math.Abs(float64(cL[s][k]-nfcIdentity[s][k])) > 0.02 {
				t.Fatalf("coefficients at the gate edge not ~identity: s%d k%d = %v", s, k, cL[s][k])
			}
		}
	}
}

// A lateral source swept 2 m -> 0.25 m: the bus-level ILD must grow
// monotonically and land on the model's proximity ILD; and the sweep must
// be free of frame-boundary clicks (zipper) despite per-frame coefficient
// updates and delay ramps.
func TestNFCSweepILDAndZipper(t *testing.T) {
	setTestDelayModel(t)
	prevOff := common.BilateralEarOffsetM
	common.BilateralEarOffsetM = 0.0875
	defer func() { common.BilateralEarOffsetM = prevOff }()

	cfg := m1Config()
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	source := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 1)

	mixer, mixerR, reverbMixer := newTestMixers(cfg)
	frameSize := cfg.FrameSize
	bus := listener.BusSamples()

	// 500 Hz tone: smooth input, so any output discontinuity is ours.
	const nFrames = 100
	phase := 0.0
	step := 2 * math.Pi * 500.0 / common.SAMPLE_RATE
	var ilds []float64
	var maxInFrameDiff, maxBoundaryDiff float64
	var lastL float32
	distFor := func(frame int) float64 { // 2.2 m -> 0.25 m over the sweep (fast: ~4 m/s)
		return 2.2 - (2.2-0.25)*float64(frame)/float64(nFrames-1)
	}

	for frame := 0; frame < nFrames; frame++ {
		d := distFor(frame)
		source.SetPosition(common.Position{X: 0.5, Y: 0.5 + d/cfg.Size, Z: 0.5}) // az 90
		for j := range source.Input {
			source.Input[j] = float32(math.Sin(phase))
			phase += step
		}
		source.PushInputRing()
		listener.EncodePeers([]*Encoder{source}, mixer, mixerR, reverbMixer)

		wL := listener.Output[:frameSize]
		wR := listener.Output[bus : bus+frameSize]
		var eL, eR float64
		for i := range wL {
			eL += float64(wL[i]) * float64(wL[i])
			eR += float64(wR[i]) * float64(wR[i])
		}
		if frame > 2 { // past fade-in
			ilds = append(ilds, 10*math.Log10(eL/eR))
			if diff := math.Abs(float64(wL[0] - lastL)); diff > maxBoundaryDiff {
				maxBoundaryDiff = diff
			}
			for i := 1; i < frameSize; i++ {
				if diff := math.Abs(float64(wL[i] - wL[i-1])); diff > maxInFrameDiff {
					maxInFrameDiff = diff
				}
			}
		}
		lastL = wL[frameSize-1]
	}

	// ILD growth: near-zero far out, matching the model's proximity ILD
	// near in (DC ILD at 0.25 m = 9.42 dB; the 500 Hz broadband value sits
	// just under it), rising monotonically within measurement wiggle.
	if math.Abs(ilds[0]) > 0.3 {
		t.Fatalf("ILD at 2.2 m = %.2f dB, want ~0", ilds[0])
	}
	final := ilds[len(ilds)-1]
	if final < 8.0 || final > 10.5 {
		t.Fatalf("ILD at 0.25 m = %.2f dB, model predicts ~9.4 dB", final)
	}
	for i := 1; i < len(ilds); i++ {
		if ilds[i] < ilds[i-1]-0.25 {
			t.Fatalf("ILD not monotone at step %d: %.2f -> %.2f", i, ilds[i-1], ilds[i])
		}
	}

	// Zipper gate: frame-boundary steps must not exceed the largest step
	// the signal itself takes within frames (with a small margin for the
	// coefficient update landing exactly on the boundary).
	if maxBoundaryDiff > maxInFrameDiff*1.5 {
		t.Fatalf("frame-boundary discontinuity %.5f vs in-frame max %.5f — zipper",
			maxBoundaryDiff, maxInFrameDiff)
	}
	t.Logf("ILD 2.2m %.2f dB -> 0.25m %.2f dB; boundary/in-frame diff %.5f/%.5f",
		ilds[0], final, maxBoundaryDiff, maxInFrameDiff)
}
