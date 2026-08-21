//go:build (linux || darwin) && xsmm

package gemm

// The libxsmm-specific gate: every production shape must be served by a
// JIT kernel, never the pure-Go fallback. Running this test inside the
// production container is the PROT_EXEC / W^X check the plan calls for
// (M9.3/M9.6): if the container denies executable JIT pages, kernels
// come back NULL, EncodeFade falls back, and this fails loudly.

import (
	"math/rand"
	"testing"
)

func TestJITServesProductionShapes(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	for _, nChannels := range testChannels {
		Predispatch(nChannels, 128)
	}

	jit0, pure0 := Served()
	calls := 0
	for _, nChannels := range testChannels {
		for _, nInputs := range testInputs {
			inputs, weights, prev := randomCase(rng, nInputs, nChannels, testSamples)
			out := make([]float32, nChannels*testSamples)
			temp := make([]float32, nChannels*testSamples)
			EncodeFade(nInputs, nChannels, nInputs, testSamples,
				inputs, weights, prev, out, temp)
			calls++
		}
	}
	jit, pure := Served()
	if pure != pure0 {
		t.Fatalf("%d/%d production-shape calls fell back to pure Go — libxsmm JIT unavailable (PROT_EXEC denied in this environment?)",
			pure-pure0, calls)
	}
	if jit-jit0 != int64(calls) {
		t.Fatalf("JIT served %d of %d calls", jit-jit0, calls)
	}
}
