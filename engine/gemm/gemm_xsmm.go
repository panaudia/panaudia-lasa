//go:build (linux || darwin) && xsmm

package gemm

// libxsmm backend (M9.3, plan/m9-saf-exit/plan.md) — the x86 Linux prod
// GEMM per the plan/gemm-backends/ decision: JIT, shape-specialised,
// stateless/reentrant kernels, ~1.5–4× OpenBLAS on every production shape
// (Zen3 sweep). Build against the pinned SHA (see
// plan/gemm-backends/bench/README.md, "Pinned dependency versions") —
// the libxsmm_dispatch_gemm API only exists on the unreleased 2.0 line.
//
// Row-major C[M×N] = W[M×K]·In[K×N] maps to libxsmm's column-major kernel
// via the operand swap C'(N×M) = In(N×K)·W(K×M): dispatch m=N, n=M, k=K
// and call kernel(in, w, out) — a zero-cost relabel (same layout as the
// parity-validated bench backend, plan/gemm-backends/bench/backend_libxsmm.go).
//
// Kernels are pre-dispatched per (M, K) off the audio thread — NewMixer
// calls Predispatch(channels, maxSources) at construction. A cache miss on
// the hot path (predispatch skipped) JITs lazily (libxsmm's registry is
// internally locked); if JIT is unavailable (e.g. no PROT_EXEC — see the
// plan's container check) every call falls back to the pure-Go path with
// one warning.

/*
#cgo CFLAGS: -O2 -I/usr/local/include
#cgo LDFLAGS: -L/usr/local/lib -lxsmm -ldl -lpthread -lm
#include <string.h>
#include <libxsmm.h>

#define PAH_GEMM_N 240      // FRAME_SIZE; init() asserts the match
#define PAH_GEMM_MAXM 40    // > 36 ch (order 5)
#define PAH_GEMM_MAXK 512   // >= MaxSources with headroom

static libxsmm_gemmfunction pah_kernels[PAH_GEMM_MAXM][PAH_GEMM_MAXK + 1];

static libxsmm_gemmfunction pah_xsmm_dispatch_one(int M, int K)
{
    libxsmm_init();
    libxsmm_gemm_shape shape = libxsmm_create_gemm_shape(
        PAH_GEMM_N, M, K,             // m, n, k (operand-swapped)
        PAH_GEMM_N, K, PAH_GEMM_N,    // lda, ldb, ldc
        LIBXSMM_DATATYPE_F32, LIBXSMM_DATATYPE_F32,
        LIBXSMM_DATATYPE_F32, LIBXSMM_DATATYPE_F32);
    libxsmm_gemmfunction f = libxsmm_dispatch_gemm(shape,
        LIBXSMM_GEMM_FLAG_NONE | LIBXSMM_GEMM_FLAG_BETA_0,
        (libxsmm_bitfield)0);
    pah_kernels[M][K] = f;
    return f;
}

// Pre-dispatch every K in 1..maxK for channel count M; returns the number
// of shapes JIT actually served (== maxK unless JIT is unavailable).
static int pah_xsmm_predispatch(int M, int maxK)
{
    int ok = 0;
    for (int K = 1; K <= maxK; ++K)
        if (pah_xsmm_dispatch_one(M, K) != NULL)
            ++ok;
    return ok;
}

// Returns 1 if a JIT kernel served the call, 0 if the caller must fall
// back. Runs both GEMMs and the crossfade in this single cgo crossing.
// The fade is verbatim from the retired panaudia_utils_internal_encode
// (mixed float/double arithmetic — see gemm.go's fadeCombine note).
static int pah_xsmm_encode_fade(int M, int K,
        const float *inputs, const float *weights, const float *prevWeights,
        float *output, float *temp)
{
    libxsmm_gemmfunction f = pah_kernels[M][K];
    if (f == NULL) {
        f = pah_xsmm_dispatch_one(M, K); // predispatch missed; registry is locked internally
        if (f == NULL)
            return 0; // JIT unavailable for this shape
    }
    libxsmm_gemm_param p;
    memset(&p, 0, sizeof(p));
    p.a.primary = (void *)inputs;
    p.b.primary = (void *)weights;
    p.c.primary = (void *)output;
    f(&p);
    p.b.primary = (void *)prevWeights;
    p.c.primary = (void *)temp;
    f(&p);

    float div = (float)(PAH_GEMM_N - 1);
    for (int i = 0; i < M; i++) {
        for (int j = 0; j < PAH_GEMM_N; j++) {
            float v = ((float)j) / div;
            int index = (i * PAH_GEMM_N) + j;
            output[index] = (v * output[index]) + ((1.0 - v) * temp[index]);
        }
    }
    return 1;
}
*/
import "C"

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// Backend names the compiled GEMM provider, for startup logs.
const Backend = "libxsmm"

// Call counters: which path served EncodeFade. Diagnostic surface for the
// M9.6 prod-container JIT/PROT_EXEC check (xsmm_test.go fails if pure Go
// served any production shape) and for post-deploy sanity logging.
var servedJIT, servedPureGo atomic.Int64

// Served reports (JIT-kernel calls, pure-Go-fallback calls) since start.
func Served() (jit, pureGo int64) {
	return servedJIT.Load(), servedPureGo.Load()
}

func init() {
	if C.PAH_GEMM_N != common.FRAME_SIZE {
		panic("gemm: PAH_GEMM_N != common.FRAME_SIZE")
	}
}

// Predispatch JIT-compiles the (nChannels, K) kernel family off the audio
// thread; NewMixer calls it at construction. Idempotent, internally locked.
func Predispatch(nChannels, maxSources int) {
	if nChannels >= C.PAH_GEMM_MAXM || maxSources > C.PAH_GEMM_MAXK {
		slog.Warn("gemm: shape exceeds the libxsmm kernel table; those calls use the pure-Go path",
			"m", nChannels, "maxK", maxSources)
		return
	}
	ok := int(C.pah_xsmm_predispatch(C.int(nChannels), C.int(maxSources)))
	if ok < maxSources {
		slog.Warn("gemm: libxsmm JITed only some kernels (JIT unavailable? check PROT_EXEC); missing shapes fall back to pure Go",
			"jitted", ok, "wanted", maxSources, "m", nChannels)
	}
}

var warnFallbackOnce sync.Once

// EncodeFade — see the package doc for the contract. One cgo call for the
// GEMM pair + fade; pure-Go fallback for shapes outside the kernel table
// (nSamples != FRAME_SIZE, padded weights, JIT unavailable).
func EncodeFade(nInputs, nChannels, nMaxInputs, nSamples int,
	inputs, weights, prevWeights, output, temp []float32) {
	checkShapes(nInputs, nChannels, nMaxInputs, nSamples,
		inputs, weights, prevWeights, output, temp)
	if nSamples == C.PAH_GEMM_N && nMaxInputs == nInputs &&
		nChannels < C.PAH_GEMM_MAXM && nInputs >= 1 && nInputs <= C.PAH_GEMM_MAXK {
		if C.pah_xsmm_encode_fade(C.int(nChannels), C.int(nInputs),
			(*C.float)(&inputs[0]),
			(*C.float)(&weights[0]),
			(*C.float)(&prevWeights[0]),
			(*C.float)(&output[0]),
			(*C.float)(&temp[0])) != 0 {
			servedJIT.Add(1)
			return
		}
		warnFallbackOnce.Do(func() {
			slog.Warn("gemm: libxsmm JIT unavailable; encode running on the pure-Go path (check PROT_EXEC / W^X in this container)")
		})
	}
	servedPureGo.Add(1)
	encodeFadePureGo(nInputs, nChannels, nMaxInputs, nSamples,
		inputs, weights, prevWeights, output, temp)
}
