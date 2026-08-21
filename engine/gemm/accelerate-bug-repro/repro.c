/*
 * Reproducer: Accelerate cblas_sgemm results are corrupted when a
 * signal is delivered to the calling thread mid-computation on Apple
 * silicon (AMX state apparently not preserved across signal delivery).
 *
 * Observed on: Darwin 25.5.0, Apple silicon.
 *
 * Discovery path: a Go audio renderer calling sgemm from a worker pool
 * saw ~1% corrupted results; the Go runtime's async-preemption signals
 * (SIGURG) turned out to be the trigger — GODEBUG=asyncpreemptoff=1
 * makes the corruption vanish. This standalone C case proves it with
 * plain pthreads + SIGUSR1:
 *
 *   Phase 1 — 16 threads x 200 iters x 100 rounds of identical
 *     sgemm calls (shared read-only operands, private outputs),
 *     NO signals: every result is bit-identical to the reference.
 *   Phase 2 — same load, plus one "pinger" thread delivering
 *     SIGUSR1 (empty handler) to the workers at high frequency:
 *     results differ from the reference by WHOLE MAGNITUDES (abs
 *     diffs on the order of the result values; billions of float
 *     ULPs) — corrupted computation, not summation-order rounding.
 *
 * Any signal source should do: profilers (SIGPROF), interval timers,
 * runtimes that signal their own threads (Go, JVMs). BLAS internal
 * threading is forced off; VECLIB_MAXIMUM_THREADS=1 changes nothing.
 *
 * Build:  cc -O2 -DACCELERATE_NEW_LAPACK -framework Accelerate repro.c -o repro
 * Run:    ./repro     exit 0: phase 2 corrupted as described (bug reproduced,
 *                             phase 1 clean)
 *                     exit 1: phase 1 corrupted too (stronger failure)
 *                     exit 2: nothing corrupted (bug not reproduced here)
 */

#include <Accelerate/Accelerate.h>
#include <math.h>
#include <pthread.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define M 16
#define N 240
#define K 64
#define THREADS 16
#define ROUNDS 100
#define ITERS 200

static float A[M * K];
static float B[K * N];
static float ref[M * N];

static atomic_int corrupted = 0;
static atomic_int stop_pinger = 0;

static pthread_t workers[THREADS];
static atomic_int workers_live = 0;

static unsigned int rng_state = 0x12345678u;
static float rng_float(void) {
    rng_state ^= rng_state << 13;
    rng_state ^= rng_state >> 17;
    rng_state ^= rng_state << 5;
    return ((float)(rng_state & 0xFFFFFF) / (float)0x800000) - 1.0f;
}

static void on_sigusr1(int sig) { (void)sig; /* deliberately empty */ }

static void sgemm_into(float *C) {
    cblas_sgemm(CblasRowMajor, CblasNoTrans, CblasNoTrans,
                M, N, K, 1.0f, A, K, B, N, 0.0f, C, N);
}

static void *worker(void *arg) {
    (void)arg;
    float *C = malloc(sizeof(float) * M * N);
    for (int it = 0; it < ITERS && !atomic_load(&corrupted); it++) {
        sgemm_into(C);
        if (memcmp(C, ref, sizeof(ref)) != 0) {
            double worst = 0.0;
            int worst_i = -1;
            for (int i = 0; i < M * N; i++) {
                double d = fabs((double)C[i] - (double)ref[i]);
                if (d > worst) { worst = d; worst_i = i; }
            }
            fprintf(stderr,
                "    CORRUPTED: worst abs diff %g at index %d (got %g, want %g)\n",
                worst, worst_i, (double)C[worst_i], (double)ref[worst_i]);
            atomic_store(&corrupted, 1);
        }
    }
    free(C);
    return NULL;
}

/* pinger: deliver SIGUSR1 round-robin to worker threads while they run */
static void *pinger(void *arg) {
    (void)arg;
    int i = 0;
    while (!atomic_load(&stop_pinger)) {
        if (atomic_load(&workers_live)) {
            pthread_kill(workers[i % THREADS], SIGUSR1);
            i++;
        }
        usleep(50);
    }
    return NULL;
}

static int run_phase(const char *name, int with_signals) {
    atomic_store(&corrupted, 0);
    pthread_t ping;
    if (with_signals) {
        atomic_store(&stop_pinger, 0);
        pthread_create(&ping, NULL, pinger, NULL);
    }
    int r;
    for (r = 0; r < ROUNDS && !atomic_load(&corrupted); r++) {
        for (int i = 0; i < THREADS; i++)
            pthread_create(&workers[i], NULL, worker, NULL);
        atomic_store(&workers_live, 1);
        for (int i = 0; i < THREADS; i++) pthread_join(workers[i], NULL);
        atomic_store(&workers_live, 0);
    }
    if (with_signals) {
        atomic_store(&stop_pinger, 1);
        pthread_join(ping, NULL);
    }
    int bad = atomic_load(&corrupted);
    fprintf(stderr, "%s: %s after %d round(s)\n",
            name, bad ? "CORRUPTED" : "clean", r);
    return bad;
}

int main(void) {
    for (int i = 0; i < M * K; i++) A[i] = rng_float();
    for (int i = 0; i < K * N; i++) B[i] = rng_float();

    signal(SIGUSR1, on_sigusr1);
    BLASSetThreading(BLAS_THREADING_SINGLE_THREADED);

    sgemm_into(ref);

    /* sanity: sequential repetition is bit-stable */
    float *check = malloc(sizeof(float) * M * N);
    for (int i = 0; i < 1000; i++) {
        sgemm_into(check);
        if (memcmp(check, ref, sizeof(ref)) != 0) {
            fprintf(stderr, "sequential run not deterministic — different bug\n");
            return 1;
        }
    }
    free(check);

    int p1 = run_phase("phase 1 (concurrent, no signals)", 0);
    int p2 = run_phase("phase 2 (concurrent + SIGUSR1 pinger)", 1);

    if (p1) return 1;      /* corrupted even without signals */
    if (p2) {
        fprintf(stderr,
            "\nBUG REPRODUCED: identical sgemm calls return corrupted results\n"
            "when SIGUSR1 is delivered to the calling threads mid-call; the\n"
            "same load with no signals is bit-exact.\n");
        return 0;
    }
    fprintf(stderr, "bug NOT reproduced on this system\n");
    return 2;
}
