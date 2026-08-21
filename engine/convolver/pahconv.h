/* pahconv — bilateral SH-domain overlap-save convolver (m3-design Part C).
 *
 * Decodes two 16-ch ear buses to stereo with static per-ear FIR filter
 * banks (PAHRTF taps): for each ear, sum over channels of
 * (bus channel * that ear's filter for that channel), overlap-save FFT-512
 * on 240-sample frames.
 *
 * Three objects (m3-design Q9): an immutable *set* (filter spectra, shared
 * across listeners; thread-safe for concurrent process calls), a
 * per-listener *handle* (input history + scratch; used by one goroutine at
 * a time), and one process() call per listener-frame — zero allocations.
 */
#ifndef PAHCONV_H
#define PAHCONV_H

#ifdef __cplusplus
extern "C" {
#endif

#define PAHCONV_BLOCK 240              /* samples per frame (5 ms @ 48 kHz) */
#define PAHCONV_FFT 512                /* overlap-save FFT size             */
#define PAHCONV_HIST (PAHCONV_FFT - PAHCONV_BLOCK)   /* 272 */
#define PAHCONV_MAX_TAPS (PAHCONV_HIST + 1)          /* 273 */

typedef struct pahconv_set pahconv_set;
typedef struct pahconv_handle pahconv_handle;

/* taps: [2][n_ch][filter_len] float32, ear-major (0 = left), channels in
 * ACN order — the PAHRTF FilterBank layout. Copied; caller keeps ownership.
 * Returns NULL if n_ch < 1 or filter_len outside (0, PAHCONV_MAX_TAPS]. */
pahconv_set *pahconv_set_create(const float *taps, int n_ch, int filter_len);
void pahconv_set_destroy(pahconv_set *set);

pahconv_handle *pahconv_handle_create(const pahconv_set *set);
void pahconv_handle_destroy(pahconv_handle *handle);
void pahconv_handle_reset(pahconv_handle *handle);

/* in: 2*n_ch channel pointers (bus L channels first, then bus R), each
 * PAHCONV_BLOCK samples. Writes PAHCONV_BLOCK samples to out_left (from
 * bus L / left-ear filters) and out_right (bus R / right-ear filters). */
void pahconv_process(pahconv_handle *handle, const float *const *in,
                     float *out_left, float *out_right);

#ifdef __cplusplus
}
#endif

#endif /* PAHCONV_H */
