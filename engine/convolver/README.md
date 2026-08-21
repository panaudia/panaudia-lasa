# core/convolver

Bilateral SH-domain overlap-save convolver (per-ear binaural decode).
In-repo cgo — compiled by `go build`, no external libraries. Two backends
behind one API (`pahconv.h`):

- **macOS** — vDSP (Accelerate), split-complex `fft_zrip` path.
- **everywhere else** — vendored **PFFFT** (below), z-order
  `pffft_zconvolve_accumulate` path. SSE on x86, NEON on ARM, scalar
  fallback elsewhere. This is the production Linux path (M9.1,
  `plan/m9-saf-exit/plan.md`).

Both backends implement the same overlap-save algorithm (FFT-512,
240-sample blocks, ≤273 taps); `convolver_test.go`'s float64 oracle
validates whichever backend the platform compiles.

## Vendored PFFFT

- Files: `pffft.c`, `pffft.h` — **pristine upstream, do not edit**.
- Source: https://bitbucket.org/jpommier/pffft (Julien Pommier),
  master @ `09796885cd5b9da5692242de2df0d81e5e1f3d21` (2026-01-05),
  fetched 2026-07-08.
- sha256:
  - `pffft.c` `930b664934a11bd11126ce1f6cdb9c1f55a573267fc2b9c5fed5884c6b1d07ac`
  - `pffft.h` `f07a580d03403ead8c4fbd10eb56ce1125b2a4bbd5a5eb628a58290f2f4881df`
- License: BSD-like (FFTPACK5 / UCAR) — full text in the file headers.
- Alternative if the 512-real path ever measures poorly: pocketfft
  (FFTW is excluded — GPL). See compute-backends.md §7.
