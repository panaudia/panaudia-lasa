/* See pahconv.h. Two backends behind one API:
 *   __APPLE__ — vDSP (Accelerate) overlap-save, the production mac path.
 *   otherwise — vendored PFFFT overlap-save (SSE/NEON, scalar fallback),
 *               the production Linux path (M9.1; see README.md for the
 *               vendoring pin and plan/m9-saf-exit/plan.md).
 * Both are the same algorithm: forward FFT-512 per channel over
 * (history || frame), spectral MAC against the set's static filter
 * spectra, one inverse FFT per ear, last PAHCONV_BLOCK samples valid.
 */
#include "pahconv.h"

#include <stdlib.h>
#include <string.h>

#ifdef __APPLE__
#include <Accelerate/Accelerate.h>
#define PAHCONV_LOG2_FFT 9
#define PAHCONV_BINS (PAHCONV_FFT / 2) /* split-complex length for zrip */
#else
#include "pffft.h"
#endif

struct pahconv_set {
    int n_ch; /* channels per ear */
    int filter_len;
    float *taps; /* [2][n_ch][filter_len] — kept for tests/debug */
#ifdef __APPLE__
    FFTSetup fft; /* immutable after create; sharable across threads */
    /* Packed-format spectra of the zero-padded taps, scaled by 1/(4*FFT) so
     * signal_fwd * spec -> inverse gives the plain circular convolution.
     * re[0]/im[0] are zeroed; the (real) DC and Nyquist products are handled
     * with the separate dc/ny arrays so the packed element never mixes into
     * the complex MAC. Layout: [2][n_ch][PAHCONV_BINS]. */
    float *spec_re;
    float *spec_im;
    float *dc; /* [2][n_ch] */
    float *ny; /* [2][n_ch] */
#else
    PFFFT_Setup *fft; /* read-only after create; sharable across threads */
    /* Spectra of the zero-padded taps in PFFFT's internal ("z") order —
     * exactly what pffft_zconvolve_accumulate consumes; the packed
     * DC/Nyquist pair is handled inside pffft as two independent reals.
     * Unscaled: the 1/FFT normalisation is applied in the MAC.
     * Layout: [2][n_ch][PAHCONV_FFT]. */
    float *spec;
#endif
};

struct pahconv_handle {
    const pahconv_set *set;
    float *hist; /* [2*n_ch][PAHCONV_HIST] — the only cross-frame state */
    /* scratch, so process() allocates nothing */
#ifdef __APPLE__
    float *time;   /* [PAHCONV_FFT] assembled input / output frame       */
    float *sig_re; /* [PAHCONV_BINS] signal spectrum                     */
    float *sig_im;
    float *acc_re; /* [PAHCONV_BINS] per-ear accumulator                 */
    float *acc_im;
#else
    float *time; /* [PAHCONV_FFT] assembled input / output frame  */
    float *freq; /* [PAHCONV_FFT] signal spectrum, z-order        */
    float *acc;  /* [PAHCONV_FFT] per-ear accumulator, z-order    */
    float *work; /* [PAHCONV_FFT] pffft work area                 */
#endif
};

static float *alloc_floats(size_t count)
{
    void *p = NULL;
    if (posix_memalign(&p, 64, count * sizeof(float)) != 0)
        return NULL;
    memset(p, 0, count * sizeof(float));
    return (float *)p;
}

pahconv_set *pahconv_set_create(const float *taps, int n_ch, int filter_len)
{
    if (n_ch < 1 || filter_len < 1 || filter_len > PAHCONV_MAX_TAPS)
        return NULL;
    pahconv_set *set = calloc(1, sizeof(*set));
    if (!set)
        return NULL;
    set->n_ch = n_ch;
    set->filter_len = filter_len;
    set->taps = alloc_floats((size_t)2 * n_ch * filter_len);
    if (!set->taps) {
        pahconv_set_destroy(set);
        return NULL;
    }
    memcpy(set->taps, taps, (size_t)2 * n_ch * filter_len * sizeof(float));

#ifdef __APPLE__
    set->fft = vDSP_create_fftsetup(PAHCONV_LOG2_FFT, kFFTRadix2);
    set->spec_re = alloc_floats((size_t)2 * n_ch * PAHCONV_BINS);
    set->spec_im = alloc_floats((size_t)2 * n_ch * PAHCONV_BINS);
    set->dc = alloc_floats((size_t)2 * n_ch);
    set->ny = alloc_floats((size_t)2 * n_ch);
    float *pad = alloc_floats(PAHCONV_FFT);
    if (!set->fft || !set->spec_re || !set->spec_im || !set->dc || !set->ny || !pad) {
        free(pad);
        pahconv_set_destroy(set);
        return NULL;
    }
    /* fft_zrip forward = 2*DFT; with both signal and filter transformed
     * that is a factor 4, and the zrip inverse contributes another factor
     * FFT/1 relative to the mathematical IDFT — fold 1/(4*FFT) in here. */
    const float scale = 1.0f / (4.0f * PAHCONV_FFT);
    for (int ec = 0; ec < 2 * n_ch; ec++) {
        memcpy(pad, set->taps + (size_t)ec * filter_len,
               (size_t)filter_len * sizeof(float));
        memset(pad + filter_len, 0,
               (size_t)(PAHCONV_FFT - filter_len) * sizeof(float));
        DSPSplitComplex spec = {set->spec_re + (size_t)ec * PAHCONV_BINS,
                                set->spec_im + (size_t)ec * PAHCONV_BINS};
        vDSP_ctoz((const DSPComplex *)pad, 2, &spec, 1, PAHCONV_BINS);
        vDSP_fft_zrip(set->fft, &spec, 1, PAHCONV_LOG2_FFT, kFFTDirection_Forward);
        vDSP_vsmul(spec.realp, 1, &scale, spec.realp, 1, PAHCONV_BINS);
        vDSP_vsmul(spec.imagp, 1, &scale, spec.imagp, 1, PAHCONV_BINS);
        set->dc[ec] = spec.realp[0];
        set->ny[ec] = spec.imagp[0];
        spec.realp[0] = 0.0f;
        spec.imagp[0] = 0.0f;
    }
    free(pad);
#else
    set->fft = pffft_new_setup(PAHCONV_FFT, PFFFT_REAL);
    set->spec = alloc_floats((size_t)2 * n_ch * PAHCONV_FFT);
    float *pad = alloc_floats(PAHCONV_FFT);
    float *work = alloc_floats(PAHCONV_FFT);
    if (!set->fft || !set->spec || !pad || !work) {
        free(pad);
        free(work);
        pahconv_set_destroy(set);
        return NULL;
    }
    for (int ec = 0; ec < 2 * n_ch; ec++) {
        memcpy(pad, set->taps + (size_t)ec * filter_len,
               (size_t)filter_len * sizeof(float));
        memset(pad + filter_len, 0,
               (size_t)(PAHCONV_FFT - filter_len) * sizeof(float));
        pffft_transform(set->fft, pad, set->spec + (size_t)ec * PAHCONV_FFT,
                        work, PFFFT_FORWARD);
    }
    free(pad);
    free(work);
#endif
    return set;
}

void pahconv_set_destroy(pahconv_set *set)
{
    if (!set)
        return;
#ifdef __APPLE__
    if (set->fft)
        vDSP_destroy_fftsetup(set->fft);
    free(set->spec_re);
    free(set->spec_im);
    free(set->dc);
    free(set->ny);
#else
    if (set->fft)
        pffft_destroy_setup(set->fft);
    free(set->spec);
#endif
    free(set->taps);
    free(set);
}

pahconv_handle *pahconv_handle_create(const pahconv_set *set)
{
    if (!set)
        return NULL;
    pahconv_handle *h = calloc(1, sizeof(*h));
    if (!h)
        return NULL;
    h->set = set;
    h->hist = alloc_floats((size_t)2 * set->n_ch * PAHCONV_HIST);
#ifdef __APPLE__
    h->time = alloc_floats(PAHCONV_FFT);
    h->sig_re = alloc_floats(PAHCONV_BINS);
    h->sig_im = alloc_floats(PAHCONV_BINS);
    h->acc_re = alloc_floats(PAHCONV_BINS);
    h->acc_im = alloc_floats(PAHCONV_BINS);
    if (!h->hist || !h->time || !h->sig_re || !h->sig_im || !h->acc_re || !h->acc_im) {
        pahconv_handle_destroy(h);
        return NULL;
    }
#else
    h->time = alloc_floats(PAHCONV_FFT);
    h->freq = alloc_floats(PAHCONV_FFT);
    h->acc = alloc_floats(PAHCONV_FFT);
    h->work = alloc_floats(PAHCONV_FFT);
    if (!h->hist || !h->time || !h->freq || !h->acc || !h->work) {
        pahconv_handle_destroy(h);
        return NULL;
    }
#endif
    return h;
}

void pahconv_handle_destroy(pahconv_handle *h)
{
    if (!h)
        return;
    free(h->hist);
#ifdef __APPLE__
    free(h->time);
    free(h->sig_re);
    free(h->sig_im);
    free(h->acc_re);
    free(h->acc_im);
#else
    free(h->time);
    free(h->freq);
    free(h->acc);
    free(h->work);
#endif
    free(h);
}

void pahconv_handle_reset(pahconv_handle *h)
{
    memset(h->hist, 0,
           (size_t)2 * h->set->n_ch * PAHCONV_HIST * sizeof(float));
}

static void push_history(float *hist, const float *in)
{
    /* keep the last PAHCONV_HIST samples of (hist || in) */
    memmove(hist, hist + PAHCONV_BLOCK,
            (size_t)(PAHCONV_HIST - PAHCONV_BLOCK) * sizeof(float));
    memcpy(hist + (PAHCONV_HIST - PAHCONV_BLOCK), in,
           PAHCONV_BLOCK * sizeof(float));
}

#ifdef __APPLE__

static void process_ear(pahconv_handle *h, int ear, const float *const *in,
                        float *out)
{
    const pahconv_set *set = h->set;
    const int n_ch = set->n_ch;
    memset(h->acc_re, 0, PAHCONV_BINS * sizeof(float));
    memset(h->acc_im, 0, PAHCONV_BINS * sizeof(float));
    float acc_dc = 0.0f, acc_ny = 0.0f;

    for (int ch = 0; ch < n_ch; ch++) {
        const int ec = ear * n_ch + ch;
        float *hist = h->hist + (size_t)ec * PAHCONV_HIST;
        memcpy(h->time, hist, PAHCONV_HIST * sizeof(float));
        memcpy(h->time + PAHCONV_HIST, in[ec], PAHCONV_BLOCK * sizeof(float));
        push_history(hist, in[ec]);

        DSPSplitComplex sig = {h->sig_re, h->sig_im};
        vDSP_ctoz((const DSPComplex *)h->time, 2, &sig, 1, PAHCONV_BINS);
        vDSP_fft_zrip(set->fft, &sig, 1, PAHCONV_LOG2_FFT, kFFTDirection_Forward);

        acc_dc += sig.realp[0] * set->dc[ec];
        acc_ny += sig.imagp[0] * set->ny[ec];

        /* complex MAC over bins 1..255; bin 0 contributes nothing because
         * the stored spectra have re[0]=im[0]=0. Plain C — 256 elements
         * auto-vectorize under -O2; FFTs dominate anyway. */
        const float *hr = set->spec_re + (size_t)ec * PAHCONV_BINS;
        const float *hi = set->spec_im + (size_t)ec * PAHCONV_BINS;
        const float *xr = h->sig_re;
        const float *xi = h->sig_im;
        float *ar = h->acc_re;
        float *ai = h->acc_im;
        for (int i = 0; i < PAHCONV_BINS; i++) {
            ar[i] += xr[i] * hr[i] - xi[i] * hi[i];
            ai[i] += xr[i] * hi[i] + xi[i] * hr[i];
        }
    }

    h->acc_re[0] = acc_dc;
    h->acc_im[0] = acc_ny;
    DSPSplitComplex acc = {h->acc_re, h->acc_im};
    vDSP_fft_zrip(set->fft, &acc, 1, PAHCONV_LOG2_FFT, kFFTDirection_Inverse);
    vDSP_ztoc(&acc, 1, (DSPComplex *)h->time, 2, PAHCONV_BINS);
    /* overlap-save: the first filter_len-1 (<= PAHCONV_HIST) samples of the
     * circular convolution are aliased; the last PAHCONV_BLOCK are valid. */
    memcpy(out, h->time + PAHCONV_HIST, PAHCONV_BLOCK * sizeof(float));
}

void pahconv_process(pahconv_handle *h, const float *const *in,
                     float *out_left, float *out_right)
{
    process_ear(h, 0, in, out_left);
    process_ear(h, 1, in, out_right);
}

#else /* !__APPLE__: PFFFT overlap-save, the production Linux path */

static void process_ear(pahconv_handle *h, int ear, const float *const *in,
                        float *out)
{
    const pahconv_set *set = h->set;
    const int n_ch = set->n_ch;
    /* pffft is unscaled: BACKWARD(FORWARD(x)) = FFT*x. Fold the 1/FFT
     * normalisation into the spectral MAC's scaling factor. */
    const float scale = 1.0f / (float)PAHCONV_FFT;
    memset(h->acc, 0, PAHCONV_FFT * sizeof(float));

    for (int ch = 0; ch < n_ch; ch++) {
        const int ec = ear * n_ch + ch;
        float *hist = h->hist + (size_t)ec * PAHCONV_HIST;
        /* in[ec] may be unaligned (CBuffer memory); h->time is aligned and
         * is what pffft sees. */
        memcpy(h->time, hist, PAHCONV_HIST * sizeof(float));
        memcpy(h->time + PAHCONV_HIST, in[ec], PAHCONV_BLOCK * sizeof(float));
        push_history(hist, in[ec]);

        pffft_transform(set->fft, h->time, h->freq, h->work, PFFFT_FORWARD);
        /* Spectral MAC in pffft's z-order — no reordering needed; the
         * packed DC/Nyquist entry is multiplied as two independent reals
         * inside pffft_zconvolve_accumulate. */
        pffft_zconvolve_accumulate(set->fft, h->freq,
                                   set->spec + (size_t)ec * PAHCONV_FFT,
                                   h->acc, scale);
    }

    pffft_transform(set->fft, h->acc, h->time, h->work, PFFFT_BACKWARD);
    /* overlap-save: the first filter_len-1 (<= PAHCONV_HIST) samples of the
     * circular convolution are aliased; the last PAHCONV_BLOCK are valid. */
    memcpy(out, h->time + PAHCONV_HIST, PAHCONV_BLOCK * sizeof(float));
}

void pahconv_process(pahconv_handle *h, const float *const *in,
                     float *out_left, float *out_right)
{
    process_ear(h, 0, in, out_left);
    process_ear(h, 1, in, out_right);
}

#endif
