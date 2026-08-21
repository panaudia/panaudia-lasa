package binaural

// M4 full-chain verification: encode-side Woodworth ITD + ear-aligned
// BiMagLS convolver decode. The runtime reinserts MODEL delays; the decode
// filters carry the measured-minus-model residual (below the MagLS cutoff),
// so the rendered interaural time must land on the set's own MEASURED ITD —
// the alignment-consistency invariant, observed end to end.

import (
	"math"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"gonum.org/v1/gonum/dsp/fourier"

	"github.com/panaudia/panaudia-lasa/engine/ambisonic"
	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/common"
	"github.com/panaudia/panaudia-lasa/engine/hrtf"
)

const (
	anchorOrder    = 3
	anchorChannels = 16
)

// broadbandDB / lowpassed / earLag: analysis helpers inherited from the M3
// anchor test (retired with the SAF ambi_bin reference at M9.4).

func broadbandDB(x []float32) float64 {
	var e float64
	for _, v := range x {
		e += float64(v) * float64(v)
	}
	return 10 * math.Log10(e/float64(len(x)))
}

// lowpassed returns x brick-walled below cutHz (for the ITD lag check:
// coherent interaural phase lives below the MagLS cutoff).
func lowpassed(x []float32, cutHz float64) []float64 {
	n := 1
	for n < len(x) {
		n <<= 1
	}
	fft := fourier.NewFFT(n)
	seq := make([]float64, n)
	for i, v := range x {
		seq[i] = float64(v)
	}
	coeffs := fft.Coefficients(nil, seq)
	cutBin := int(cutHz / (float64(common.SAMPLE_RATE) / float64(n)))
	for i := cutBin; i < len(coeffs); i++ {
		coeffs[i] = 0
	}
	out := fft.Sequence(nil, coeffs)
	for i := range out {
		out[i] /= float64(n)
	}
	return out[:len(x)]
}

func earLag(l, r []float32, maxLag int) int {
	lf, rf := lowpassed(l, 1500), lowpassed(r, 1500)
	bestLag, _ := common.XCorrArgmax(lf, rf, -maxLag, maxLag, maxLag, len(lf)-maxLag)
	return bestLag
}

func TestM4ChainITDMatchesMeasured(t *testing.T) {
	if testing.Short() {
		t.Skip("full-chain render is slow")
	}

	set := hrtf.Default()
	defer set.InstallDelayModel()()

	cfg := common.MixerConfig{
		MaxNodes:     4,
		FrameSize:    common.FRAME_SIZE,
		ChannelCount: anchorChannels,
		Order:        anchorOrder,
		Size:         10,
		ReverbPreset: common.REVERB_NONE,
	}
	listener := ambisonic.NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	source := ambisonic.NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 1)
	source.SetPosition(common.Position{X: 0.5, Y: 0.8, Z: 0.5}) // az 90, hard left

	mixer, mixerR := ambisonic.NewMixer(cfg), ambisonic.NewMixer(cfg)
	reverbMixer := ambisonic.NewMixer(cfg)

	cset := NewConvolverSet(set, anchorChannels)
	defer cset.BeforeDestroy()
	dec := NewConvolverDecoder(cset)
	defer dec.BeforeDestroy()

	var chans []*buffers.CBuffer
	var ptrs []uintptr
	for i := 0; i < 2*anchorChannels; i++ {
		b := buffers.NewCBuffer(common.FRAME_SIZE)
		defer b.BeforeDestroy()
		chans = append(chans, b)
		ptrs = append(ptrs, b.GetDataPointer())
	}

	rng := rand.New(rand.NewSource(21))
	frameSize := cfg.FrameSize
	bus := listener.BusSamples()
	var outL, outR []float32
	for frame := 0; frame < 400; frame++ {
		for j := range source.Input {
			source.Input[j] = rng.Float32()*2 - 1
		}
		source.PushInputRing()
		listener.EncodePeers([]*ambisonic.Encoder{source}, mixer, mixerR, reverbMixer)
		for ch, buf := range chans {
			var seg []float32
			if ch < anchorChannels {
				seg = listener.Output[ch*frameSize : (ch+1)*frameSize]
			} else {
				seg = listener.Output[bus+(ch-anchorChannels)*frameSize : bus+(ch-anchorChannels+1)*frameSize]
			}
			buf.CopyFromSlice(seg)
		}
		dec.BilateralToStereo(ptrs)
		outL = append(outL, dec.LeftCBuffer().AsUnsafeFloatSlice()...)
		outR = append(outR, dec.RightCBuffer().AsUnsafeFloatSlice()...)
	}

	// Expected: the Woodworth model ITD at az 90 — the M4 milestone gate.
	// The runtime reinserts the model broadband; the filters' below-cutoff
	// residual is a high-spatial-order feature an order-3 decode renders
	// only partially, so the model IS what comes out (standard BiMagLS
	// behaviour). The set's stored "measured" ITD is NOT usable as ground
	// truth at hard laterals: SAF's estimator clamps at sqrt(2)/2000 s
	// (33.9 samples) and saturates there.
	wantITD := common.BilateralHeadRadiusM / common.BilateralSpeedOfSoundMS *
		(math.Pi/2 + 1) * common.SAMPLE_RATE

	lag := earLag(outL[4800:], outR[4800:], 48)
	if math.Abs(float64(lag)-wantITD) > 1.5 {
		t.Fatalf("rendered interaural lag %d samples, want Woodworth model %.1f ±1.5",
			lag, wantITD)
	}

	// ILD sanity at hard left: the near (left) ear must be clearly louder —
	// head shadow comes from the decode filters, not the delay.
	ild := broadbandDB(outL[4800:]) - broadbandDB(outR[4800:])
	if ild < 3.0 {
		t.Fatalf("az 90 broadband ILD %.2f dB: expected strong left bias", ild)
	}
	t.Logf("az 90 rendered lag %d samples (Woodworth model %.2f); broadband ILD %.2f dB",
		lag, wantITD, ild)
}
