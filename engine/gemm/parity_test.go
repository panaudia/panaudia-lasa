package gemm

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// Real production shapes: M = ambisonic channels (+4 = reverb bus),
// N = 240 (FRAME_SIZE), K = active peers up to MaxSources.
var testChannels = []int{4, 9, 16, 25, 36}
var testInputs = []int{1, 2, 3, 7, 16, 50, 128}

const testSamples = 240

func randomCase(rng *rand.Rand, nInputs, nChannels, nSamples int) (inputs, weights, prev []float32) {
	inputs = make([]float32, nInputs*nSamples)
	weights = make([]float32, nChannels*nInputs)
	prev = make([]float32, nChannels*nInputs)
	for i := range inputs {
		inputs[i] = rng.Float32()*2 - 1
	}
	for i := range weights {
		weights[i] = rng.Float32()*2 - 1
		prev[i] = rng.Float32()*2 - 1
	}
	return
}

// TestOracle checks EncodeFade against an independent float64 triple-loop
// GEMM + fade. Permanent (backend-agnostic); catches layout/transpose bugs
// in any future backend (the M9.3 libxsmm operand swap especially).
func TestOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(10))
	for _, nChannels := range testChannels {
		for _, nInputs := range testInputs {
			inputs, weights, prev := randomCase(rng, nInputs, nChannels, testSamples)

			got := make([]float32, nChannels*testSamples)
			temp := make([]float32, nChannels*testSamples)
			EncodeFade(nInputs, nChannels, nInputs, testSamples,
				inputs, weights, prev, got, temp)

			div := float64(testSamples - 1)
			for c := 0; c < nChannels; c++ {
				for s := 0; s < testSamples; s++ {
					var cur, pre float64
					for k := 0; k < nInputs; k++ {
						x := float64(inputs[k*testSamples+s])
						cur += float64(weights[c*nInputs+k]) * x
						pre += float64(prev[c*nInputs+k]) * x
					}
					v := float64(s) / div
					want := v*cur + (1-v)*pre
					if math.Abs(float64(got[c*testSamples+s])-want) > 1e-4*math.Max(1, math.Abs(want)) {
						t.Fatalf("M=%d K=%d ch %d sample %d: got %g want %g",
							nChannels, nInputs, c, s, got[c*testSamples+s], want)
					}
				}
			}
		}
	}
}

// BenchmarkEncodeFade measures the unit of work — one dual-GEMM + fade at
// the canonical mid-size shape (M=16 order 3, K=64 sources, N=240).
func BenchmarkEncodeFade(b *testing.B) {
	rng := rand.New(rand.NewSource(13))
	const nChannels, nInputs = 16, 64
	inputs, weights, prev := randomCase(rng, nInputs, nChannels, testSamples)
	out := make([]float32, nChannels*testSamples)
	temp := make([]float32, nChannels*testSamples)
	Predispatch(nChannels, nInputs)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncodeFade(nInputs, nChannels, nInputs, testSamples,
			inputs, weights, prev, out, temp)
	}
}

// TestConcurrentParity hammers EncodeFade from 16 goroutines with shared
// read-only operands and per-goroutine outputs — the reentrancy contract
// every backend must hold (the OpenBLAS scratch race this package
// retires produced wrong numbers under exactly this load). Permanent.
func TestConcurrentParity(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	const nChannels, nInputs = 16, 64
	inputs, weights, prev := randomCase(rng, nInputs, nChannels, testSamples)

	ref := make([]float32, nChannels*testSamples)
	refTemp := make([]float32, nChannels*testSamples)
	EncodeFade(nInputs, nChannels, nInputs, testSamples,
		inputs, weights, prev, ref, refTemp)

	const workers, iters = 16, 200
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func() {
			out := make([]float32, nChannels*testSamples)
			temp := make([]float32, nChannels*testSamples)
			for i := 0; i < iters; i++ {
				EncodeFade(nInputs, nChannels, nInputs, testSamples,
					inputs, weights, prev, out, temp)
				for j := range ref {
					if out[j] != ref[j] {
						errCh <- fmt.Errorf("concurrent mismatch at sample %d", j)
						return
					}
				}
			}
			errCh <- nil
		}()
	}
	for w := 0; w < workers; w++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}
