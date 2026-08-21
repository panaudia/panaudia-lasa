package ambisonic

import (
	"math"
	"math/rand"
	"testing"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// TestMixNativeVsGonumParity checks that the pure-Go gonum mixing path (Mix2)
// produces the same output as the native cBLAS path (Mix) for identical inputs.
//
// It exists to validate Mix2 as a sample-for-sample drop-in for Mix before we
// consider swapping the live hot path off native OpenBLAS (see
// ../../cloud-mixer/plan/clustering-crackle/findings.md — "Fix F"). Mix2 is
// otherwise unreferenced/untested, so this is the first check that its matrix
// layout (transposed packed weights, stride) and previous→current weight
// crossfade actually match the C implementation.
//
// A correct Mix2 should match Mix to within float32 GEMM rounding (~1e-5 rel);
// a transpose/stride/fade bug diverges by order-1 and fails loudly.
//
// Requires a performance build tag so the cgo BLAS path links, e.g.:
//
//	go test -tags=accelerate ./core/ambisonic/ -run TestMixNativeVsGonumParity -v
func TestMixNativeVsGonumParity(t *testing.T) {
	mixerConfig := common.MixerConfig{
		MaxNodes:     16,
		FrameSize:    common.FRAME_SIZE,
		ChannelCount: 9, // 2nd-order ambisonics: (2+1)^2
		Order:        2,
		Size:         2,
		ReverbPreset: common.REVERB_NONE,
	}

	rng := rand.New(rand.NewSource(42))

	randFloat := func() float32 { return rng.Float32()*2 - 1 } // [-1, 1)

	for _, count := range []int{1, 2, 5, 8, 13, 16} {
		mixer := NewMixer(mixerConfig)
		mixer.Reset(count)

		for i := 0; i < count; i++ {
			input := make([]float32, mixerConfig.FrameSize)
			for j := range input {
				input[j] = randFloat()
			}
			weights := make([]float32, mixerConfig.ChannelCount)
			prevWeights := make([]float32, mixerConfig.ChannelCount)
			for c := range weights {
				weights[c] = randFloat()
				prevWeights[c] = randFloat()
			}
			mixer.AddInput(input, weights, prevWeights)
		}

		outSize := mixerConfig.ChannelCount * mixerConfig.FrameSize
		native := make([]float32, outSize)
		gonum := make([]float32, outSize)

		// Neither Mix nor Mix2 mutates the packed input/weight buffers, so both
		// can run against the same prepared mixer state (each writes its own
		// output and the shared tempMix scratch, sequentially).
		mixer.Mix(native)
		mixer.Mix2(gonum)

		var maxAbs, maxMag float64
		for i := range native {
			if d := math.Abs(float64(native[i] - gonum[i])); d > maxAbs {
				maxAbs = d
			}
			if m := math.Abs(float64(native[i])); m > maxMag {
				maxMag = m
			}
		}
		// tolerance scales with signal magnitude; floor of 1.0 keeps it sane for
		// near-silent frames.
		absTol := 1e-4 * math.Max(1.0, maxMag)
		t.Logf("count=%2d  maxAbsDiff=%.3e  maxMag=%.3e  absTol=%.3e", count, maxAbs, maxMag, absTol)
		if maxAbs > absTol {
			t.Errorf("count=%d: native (Mix) vs gonum (Mix2) diverge: maxAbsDiff=%.6e > tol=%.6e",
				count, maxAbs, absTol)
		}
	}
}
