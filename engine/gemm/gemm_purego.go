package gemm

import (
	"gonum.org/v1/gonum/blas"
	"gonum.org/v1/gonum/blas/blas32"
)

// encodeFadePureGo is the pure-Go EncodeFade implementation: the gonum
// backend's whole body, and the libxsmm backend's fallback for shapes its
// JIT table can't serve. Compiled on every platform (dead code on darwin).
func encodeFadePureGo(nInputs, nChannels, nMaxInputs, nSamples int,
	inputs, weights, prevWeights, output, temp []float32) {
	gemmRowMajor(nInputs, nChannels, nMaxInputs, nSamples, inputs, weights, output)
	gemmRowMajor(nInputs, nChannels, nMaxInputs, nSamples, inputs, prevWeights, temp)
	fadeCombine(output, temp, nChannels, nSamples)
}

func gemmRowMajor(nInputs, nChannels, nMaxInputs, nSamples int,
	inputs, weights, out []float32) {
	// k = nInputs with lda = nMaxInputs, exactly the cblas_sgemm call this
	// ports (the weights rows may be wider than the active input count).
	blas32.Gemm(blas.NoTrans, blas.NoTrans, 1.0,
		blas32.General{Rows: nChannels, Cols: nInputs, Data: weights, Stride: nMaxInputs},
		blas32.General{Rows: nInputs, Cols: nSamples, Data: inputs, Stride: nSamples},
		0.0,
		blas32.General{Rows: nChannels, Cols: nSamples, Data: out, Stride: nSamples})
}
