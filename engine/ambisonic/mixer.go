package ambisonic

import (
	"log/slog"
	"sync"

	"github.com/panaudia/panaudia-lasa/engine/common"
	"github.com/panaudia/panaudia-lasa/engine/gemm"
	"gonum.org/v1/gonum/blas"
	"gonum.org/v1/gonum/blas/blas32"
)

// The mixing path is MixerConfig.PureGoMixer: the pure-Go gonum path
// (Mix2) over the core/gemm dispatch. Kept as the A/B safety hatch and
// benchmark baseline (plan/m9-saf-exit/plan.md M9.2); every core/gemm
// backend is reentrant, so the OpenBLAS shared-scratch race that created
// this switch is gone (history:
// ../../../cloud-mixer/plan/clustering-crackle/findings.md, Fix F).
// Output is sample-for-sample equivalent (see mix_parity_test.go). It is
// configuration passed in, never read from the environment here.

// logBackendOnce reports the mixing path in use — from NewMixer, not
// package init, so it lands in whatever logger main has installed.
var logBackendOnce sync.Once

func logBackend(pureGo bool) {
	logBackendOnce.Do(func() {
		if pureGo {
			slog.Info("ambisonic mixer: pure-Go gonum BLAS path")
		} else {
			slog.Info("ambisonic mixer: GEMM backend", "backend", gemm.Backend, "target", gemm.Target())
		}
	})
}

type Mixer struct {
	inputTotalCount   int
	inputRunningCount int
	mixerConfig       common.MixerConfig

	//packed versions recreated each pass
	packedInputs          []float32
	packedWeights         []float32
	previousPackedWeights []float32

	//temp premixes
	tempMix  []float32
	MixCount int
}

func NewMixer(mixerConfig common.MixerConfig) *Mixer {
	logBackend(mixerConfig.PureGoMixer)

	mixer := Mixer{mixerConfig: mixerConfig,
		inputTotalCount: 0, inputRunningCount: 0}

	inputsSize := mixerConfig.FrameSize * mixerConfig.MaxNodes
	weightsSize := mixerConfig.ChannelCount * mixerConfig.MaxNodes
	outputSize := mixerConfig.ChannelCount * mixerConfig.FrameSize

	//Weights get packed transposed into these when you add an input
	mixer.packedInputs = make([]float32, inputsSize)
	mixer.packedWeights = make([]float32, weightsSize)
	mixer.previousPackedWeights = make([]float32, weightsSize)

	mixer.tempMix = make([]float32, outputSize)

	// JIT-compile this mixer's (channels, K) kernel family now, off the
	// audio thread (no-op on non-libxsmm backends).
	gemm.Predispatch(mixerConfig.ChannelCount, mixerConfig.MaxNodes)

	return &mixer
}

func (mixer *Mixer) Reset(count int) {
	mixer.packedInputs = mixer.packedInputs[:0]
	mixer.packedWeights = mixer.packedWeights[:count*mixer.mixerConfig.ChannelCount]
	mixer.previousPackedWeights = mixer.previousPackedWeights[:count*mixer.mixerConfig.ChannelCount]
	mixer.inputTotalCount = count
	mixer.inputRunningCount = 0
}

func (mixer *Mixer) AddInput(input []float32, weights []float32, previousWeights []float32) {
	mixer.packedInputs = append(mixer.packedInputs, input...)
	mixer.packWeights(weights, previousWeights)
}

// AddInputDelayed packs one source row read through a fractional delay
// (M4 encode-side ITD, plan design decision 4: the delay stage REPLACES the
// pack copy — one read, one write per sample, no intermediate buffers).
// ring is the source's shared history ring (layout: [DelayRingHistory
// history | FrameSize current frame | 1 zero spare]); the delay ramps
// linearly from prevDelay to delay across the frame, mirroring the weight
// crossfade (sample 0 at prevDelay — last frame's end — landing exactly on
// delay at the final sample), with linear interpolation between samples.
func (mixer *Mixer) AddInputDelayed(ring []float32, prevDelay, delay float32,
	weights []float32, previousWeights []float32) {
	frameSize := mixer.mixerConfig.FrameSize
	n0 := len(mixer.packedInputs)
	mixer.packedInputs = mixer.packedInputs[:n0+frameSize]
	row := mixer.packedInputs[n0:]

	if delay == prevDelay && delay == float32(int32(delay)) {
		// Static integer delay (median-plane sources sit here every frame):
		// a straight copy, keeping the M2 bit-exactness anchor meaningful.
		start := DelayRingHistory - int(delay)
		copy(row, ring[start:start+frameSize])
	} else {
		step := (delay - prevDelay) / float32(frameSize-1)
		d := prevDelay
		for n := range row {
			pos := float32(DelayRingHistory+n) - d
			i := int(pos)
			frac := pos - float32(i)
			row[n] = ring[i] + frac*(ring[i+1]-ring[i])
			d += step
		}
	}
	mixer.packWeights(weights, previousWeights)
}

// AddInputDelayedNFC is AddInputDelayed with the M6 near-field biquad
// cascade fused in (plan design decision 4: one read, one write per sample
// — the delay interpolation feeds the biquads feeds the packed row).
// coeffs holds the gate-blended per-ear sections from nfcCoefficients;
// state is the per-(pair, ear) DF2T state (2 sections × 2), owned by the
// listener encoder and flushed against denormals here (the biquads run in
// pure Go — no FTZ/DAZ — and decaying tails in a quiet scene would
// otherwise hit 10-100× denormal slowdowns exactly where nobody
// benchmarks; plan design decision 3).
func (mixer *Mixer) AddInputDelayedNFC(ring []float32, prevDelay, delay float32,
	coeffs *[nfcNumSections][5]float32, state *[2 * nfcNumSections]float32,
	weights []float32, previousWeights []float32) {
	frameSize := mixer.mixerConfig.FrameSize
	n0 := len(mixer.packedInputs)
	mixer.packedInputs = mixer.packedInputs[:n0+frameSize]
	row := mixer.packedInputs[n0:]

	b00, b01, b02, a01, a02 := coeffs[0][0], coeffs[0][1], coeffs[0][2], coeffs[0][3], coeffs[0][4]
	b10, b11, b12, a11, a12 := coeffs[1][0], coeffs[1][1], coeffs[1][2], coeffs[1][3], coeffs[1][4]
	s00, s01, s10, s11 := state[0], state[1], state[2], state[3]

	step := (delay - prevDelay) / float32(frameSize-1)
	d := prevDelay
	for n := range row {
		pos := float32(DelayRingHistory+n) - d
		i := int(pos)
		frac := pos - float32(i)
		x := ring[i] + frac*(ring[i+1]-ring[i])
		d += step

		// Two cascaded transposed-direct-form-II biquads.
		y := b00*x + s00
		s00 = b01*x - a01*y + s01
		s01 = b02*x - a02*y
		x = y
		y = b10*x + s10
		s10 = b11*x - a11*y + s11
		s11 = b12*x - a12*y
		row[n] = y
	}

	// Denormal flush: one branch per state per frame.
	if s00 < 1e-20 && s00 > -1e-20 {
		s00 = 0
	}
	if s01 < 1e-20 && s01 > -1e-20 {
		s01 = 0
	}
	if s10 < 1e-20 && s10 > -1e-20 {
		s10 = 0
	}
	if s11 < 1e-20 && s11 > -1e-20 {
		s11 = 0
	}
	state[0], state[1], state[2], state[3] = s00, s01, s10, s11

	mixer.packWeights(weights, previousWeights)
}

func (mixer *Mixer) packWeights(weights []float32, previousWeights []float32) {
	for i := range weights {
		transposeIndex := (i * mixer.inputTotalCount) + mixer.inputRunningCount
		mixer.packedWeights[transposeIndex] = weights[i]
		mixer.previousPackedWeights[transposeIndex] = previousWeights[i]
	}
	mixer.inputRunningCount++
}

func (mixer *Mixer) Mix(output []float32) {

	if mixer.mixerConfig.PureGoMixer {
		mixer.Mix2(output)
	} else {
		// In-repo GEMM dispatch (core/gemm, M9.2) — same contract as the
		// retired panaudia_utils_internal_encode; backend per build target.
		gemm.EncodeFade(mixer.inputTotalCount,
			mixer.mixerConfig.ChannelCount,
			mixer.inputTotalCount,
			mixer.mixerConfig.FrameSize,
			mixer.packedInputs,
			mixer.packedWeights,
			mixer.previousPackedWeights,
			output,
			mixer.tempMix)
	}

	mixer.MixCount += mixer.inputTotalCount
}

func (mixer *Mixer) Mix2(output []float32) {

	AmbisonicEncode(mixer.inputTotalCount,
		mixer.mixerConfig.ChannelCount,
		mixer.inputTotalCount,
		mixer.mixerConfig.FrameSize,
		mixer.packedInputs,
		mixer.previousPackedWeights,
		output)

	AmbisonicEncode(mixer.inputTotalCount,
		mixer.mixerConfig.ChannelCount,
		mixer.inputTotalCount,
		mixer.mixerConfig.FrameSize,
		mixer.packedInputs,
		mixer.packedWeights,
		mixer.tempMix)

	var v float32
	var index int
	div := float32(mixer.mixerConfig.FrameSize - 1)
	// Fused Fade Operation:
	for i := 0; i < mixer.mixerConfig.ChannelCount; i++ {
		for j := 0; j < mixer.mixerConfig.FrameSize; j++ {
			v = float32(j) / div
			index = (i * mixer.mixerConfig.FrameSize) + j
			output[index] = v*mixer.tempMix[index] + ((1.0 - v) * output[index])
		}
	}
}

func AmbisonicEncode(nInputs int,
	nOutputs int,
	nMaxInputs int,
	nSamples int,
	inputs []float32,
	weights []float32,
	outputs []float32) {

	// Ensure the sizes of weights and inputs match the expected dimensions
	// inputs:  [nInputs, nSamples]
	// weights: [nOutputs, nMaxInputs]
	// outputs: [nOutputs, nSamples]

	// Create BLAS-compatible matrices
	weightsMatrix := blas32.General{
		Rows:   nOutputs,
		Cols:   nMaxInputs,
		Data:   weights,
		Stride: nMaxInputs,
	}
	inputMatrix := blas32.General{
		Rows:   nInputs,
		Cols:   nSamples,
		Data:   inputs,
		Stride: nSamples,
	}
	outputMatrix := blas32.General{
		Rows:   nOutputs,
		Cols:   nSamples,
		Data:   outputs,
		Stride: nSamples,
	}

	//fmt.Printf("Gemm in go")

	// Perform the matrix multiplication using sgemm equivalent in Go
	// Float32 version of GEMM (Matrix-Matrix Multiplication):
	//
	// cblas_sgemm( CblasRowMajor,
	//              NoTrans, NoTrans,
	//              nOutputs, nSamples, nInputs,
	//              1.0f,
	//              weights, nMaxInputs,
	//              inputs,  nSamples,
	//              0.0f,
	//              outputs, nSamples );
	//
	// In Go: Gemm(settings)
	blas32.Gemm(blas.NoTrans, blas.NoTrans, 1.0,
		weightsMatrix, inputMatrix, 0.0, outputMatrix)
}
