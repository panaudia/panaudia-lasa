//go:build !darwin && !xsmm

package gemm

// Backend names the compiled GEMM provider, for startup logs.
const Backend = "gonum"

// Predispatch is a no-op for the pure-Go backend (the libxsmm backend
// JIT-compiles one kernel per (channels, K≤maxSources) here).
func Predispatch(nChannels, maxSources int) {}

// EncodeFade — see the package doc for the contract. Pure-Go fallback;
// the linux+xsmm backend (M9.3) takes precedence on the prod target.
func EncodeFade(nInputs, nChannels, nMaxInputs, nSamples int,
	inputs, weights, prevWeights, output, temp []float32) {
	checkShapes(nInputs, nChannels, nMaxInputs, nSamples,
		inputs, weights, prevWeights, output, temp)
	encodeFadePureGo(nInputs, nChannels, nMaxInputs, nSamples,
		inputs, weights, prevWeights, output, temp)
}

// Target names the code path in use; this backend has only one.
func Target() string { return "gonum" }
