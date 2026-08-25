package jitterlab

// v3.go — the retired v3 jitter buffer (asymmetric adaptive window),
// moved here from buffers/ at the v4 production swap. It exists so the
// measured v3 baseline (plan/jitter-v3-baseline.md, the source of v4's
// acceptance criteria) stays reproducible. Never used by production. The floor
// is fixed (R+S); two independent allowances widen/narrow the window — L on the
// late side (costs latency), H on the bunch side (costs capacity) — driven
// purely by counting the buffer's own ±1 corrections over a tumbling window of
// reads. No wall-clock, no rates, no decay constants. See
// plan/history/jitter-buffer/design_v3.md for the full design.

import (
	"sync/atomic"
	"time"
)

// v3 controller tuning — collected here (not scattered as magic numbers) per
// design_v3.md §10 so they can be observed and adjusted from soak. Starting
// guesses; refine later. None of these is a wall-clock value: the window is a
// read count, the threshold a correction count, the steps are sample counts.
const (
	v3WindowReads      = 400 // N: tumbling-window length, in reads
	v3WidenThreshold   = 5   // corrections/side/window to call it jitter
	v3WidenStepMs      = 2   // eager up
	v3NarrowStepMicros = 500 // 0.5 ms — reluctant down (¼ of widen)
)

// v3 geometry defaults (zero-valued config fields fall back to these). High-side
// allowances default relative to the writer frame W and are filled in the
// constructor.
const (
	v3DefaultSampleRate      = 48000
	v3DefaultChannels        = 1
	v3DefaultWriterMs        = 20
	v3DefaultReaderMs        = 5
	v3DefaultSafetyMs        = 1
	v3DefaultLowInitMs       = 5  // warm-start guess (demoted role of v2 JitterTolerance)
	v3DefaultLowMinMs        = 2  // baseline low cushion
	v3DefaultLowMaxMs        = 30 // latency ceiling
	v3DefaultHighMaxMultiple = 3  // H_max = 3*W
)

// V3Config configures a V3Buffer. Zero-valued fields use the
// defaults above. R and W are declared by the caller (it knows its callback and
// codec frame sizes; they are stable) — see design_v3.md §10. The ring is
// allocated for the worst case independently, so a write larger than W is only
// ever a recoverable snap, never corruption.
type V3Config struct {
	SampleRate      int
	NumChannels     int
	WriterFrameSize time.Duration // W. default 20ms.
	ReaderFrameSize time.Duration // R. default 5ms.
	Safety          time.Duration // S, floor pad above the underrun edge. default 1ms.

	// Low-side (late-jitter) allowance bounds — the latency-costing side.
	LowInit time.Duration // warm-start L. default 5ms.
	LowMin  time.Duration // adapt-down floor. default 2ms.
	LowMax  time.Duration // latency ceiling. default 30ms.

	// High-side (bunch) allowance bounds — costs only capacity. Defaults are
	// relative to W: HighInit = HighMin = W, HighMax = 3*W.
	HighInit time.Duration
	HighMin  time.Duration
	HighMax  time.Duration
}

// V3Buffer is a lock-free single-producer single-consumer jitter buffer
// whose operating window is fixed at the floor (R+S) and widens/narrows
// asymmetrically — L on the late side, H on the bunch side — driven by counting
// its own ±1 corrections (no wall-clock). See design_v3.md.
//
// Concurrency: exactly one producer calls Write; exactly one consumer calls
// Read. The producer touches only writePos and the ring. currentL/currentH are
// reader-written atomics readable by stats observers; everything else the
// reader uses is single-writer.
type V3Buffer struct {
	// Immutable geometry (per-channel frames). Levels are derived per-Read from
	// these plus the live L/H — see levels().
	capacity int64 // ring size in frames
	floor    int64 // R + S — fixed minimum operating point
	w        int64 // W — structural sawtooth amplitude / snap headroom / high gap
	nc       int   // channels per frame
	sr       int   // sample rate

	// Immutable adaptive bounds (frames).
	lMin, lMax int64
	hMin, hMax int64

	// Immutable controller constants (Phase 3 consumes these).
	windowReads    int64 // N
	widenThreshold int64 // corrections/side/window
	widenStep      int64 // frames, eager up
	narrowStep     int64 // frames, reluctant down

	// SPSC heads — atomic, cumulative (never wrap). Index into data with
	// (pos % capacity) * nc.
	writePos atomic.Int64
	readPos  atomic.Int64

	// Storage: capacity * nc interleaved floats.
	data []float32

	// Live adaptive window (reader-written; atomics for stats observers).
	currentL atomic.Int64
	currentH atomic.Int64

	// Last completed tumbling window's correction counts, published by adapt()
	// for observation. Reader-written atomics, read by stats observers.
	lastWinInserts atomic.Int64
	lastWinDrops   atomic.Int64

	// Reader-owned tumbling-window controller state. The ±1 splices bump
	// insertCount/dropCount; adapt() ticks readsThisWindow each Read and, every
	// windowReads, runs decide() then resets all three. Not part of the SPSC
	// contract (single writer: the reader thread).
	insertCount     int64
	dropCount       int64
	readsThisWindow int64

	// Stats — atomic counters, written from the reader thread.
	statsUnderruns atomic.Int64
	statsOverruns  atomic.Int64
	statsLaps      atomic.Int64
	statsDropped   atomic.Int64
	statsInserted  atomic.Int64
}

// defaultVal returns def when v is zero, else v.
func defaultVal(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// defaultDuration returns def when v is the zero value, else v.
func defaultDuration(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return v
}

// NewV3Buffer constructs a V3Buffer from cfg. Panics on
// non-sensical inputs (zero/negative frame sizes, mis-ordered bounds).
func NewV3Buffer(cfg V3Config) *V3Buffer {
	sr := defaultVal(cfg.SampleRate, v3DefaultSampleRate)
	nc := defaultVal(cfg.NumChannels, v3DefaultChannels)
	w := defaultDuration(cfg.WriterFrameSize, v3DefaultWriterMs*time.Millisecond)
	r := defaultDuration(cfg.ReaderFrameSize, v3DefaultReaderMs*time.Millisecond)
	s := defaultDuration(cfg.Safety, v3DefaultSafetyMs*time.Millisecond)

	lInit := defaultDuration(cfg.LowInit, v3DefaultLowInitMs*time.Millisecond)
	lMin := defaultDuration(cfg.LowMin, v3DefaultLowMinMs*time.Millisecond)
	lMax := defaultDuration(cfg.LowMax, v3DefaultLowMaxMs*time.Millisecond)

	// High-side defaults are W-relative (cheap headroom, sized to a bunch).
	hInit := cfg.HighInit
	if hInit == 0 {
		hInit = w
	}
	hMin := cfg.HighMin
	if hMin == 0 {
		hMin = w
	}
	hMax := cfg.HighMax
	if hMax == 0 {
		hMax = v3DefaultHighMaxMultiple * w
	}

	durToFrames := func(d time.Duration) int64 {
		return int64(d) * int64(sr) / int64(time.Second)
	}

	W := durToFrames(w)
	R := durToFrames(r)
	S := durToFrames(s)
	Lmin, Linit, Lmax := durToFrames(lMin), durToFrames(lInit), durToFrames(lMax)
	Hmin, Hinit, Hmax := durToFrames(hMin), durToFrames(hInit), durToFrames(hMax)

	if R <= 0 || W <= 0 {
		panic("V3Buffer: WriterFrameSize and ReaderFrameSize must be > 0")
	}
	if S < 0 {
		panic("V3Buffer: Safety must be >= 0")
	}
	if !(0 <= Lmin && Lmin <= Linit && Linit <= Lmax) {
		panic("V3Buffer: require 0 <= LowMin <= LowInit <= LowMax")
	}
	if !(0 <= Hmin && Hmin <= Hinit && Hinit <= Hmax) {
		panic("V3Buffer: require 0 <= HighMin <= HighInit <= HighMax")
	}
	// snapTarget (T+W) must land below the overrun line (T+2W+H) so snapping out
	// of an overrun doesn't re-trip it. The gap is W+H; with H ≥ Hmin ≥ 0 the
	// tightest case is H=0, so checking W>0 suffices. Asserted explicitly per
	// design_v3.md §3 / plan_v3.md Phase 1.
	if W <= 0 {
		panic("V3Buffer: require snapTarget < overrunAt (0 < W+H)")
	}

	floor := R + S

	// Capacity is worst-cased: the overrun line at full adaptation, doubled,
	// plus headroom for a write/read overshoot and lap safety. A burst larger
	// than this is clipped by Write and trips the lap branch — both recover.
	maxWR := W
	if R > maxWR {
		maxWR = R
	}
	bandTopMax := R + S + Lmax + 2*W + Hmax // = overrunAt at L_max, H_max
	capacity := 2*bandTopMax + 2*maxWR

	j := &V3Buffer{
		capacity:       capacity,
		floor:          floor,
		w:              W,
		nc:             nc,
		sr:             sr,
		lMin:           Lmin,
		lMax:           Lmax,
		hMin:           Hmin,
		hMax:           Hmax,
		windowReads:    v3WindowReads,
		widenThreshold: v3WidenThreshold,
		widenStep:      durToFrames(v3WidenStepMs * time.Millisecond),
		narrowStep:     int64(sr) * v3NarrowStepMicros / 1_000_000,
		data:           make([]float32, capacity*int64(nc)),
	}
	j.currentL.Store(Linit) // warm start
	j.currentH.Store(Hinit)
	return j
}

// levels derives the operating thresholds from the (loaded-once) window
// allowances l and h plus the immutable floor and writer frame. Pure function;
// all branches of Read use one consistent snapshot.
//
//	T          = floor + l            // operating target / sawtooth bottom
//	snapTarget = T + W                // recovery point for every snap
//	dropLine   = T + W + h            // drift-DROP fires above this
//	overrunAt  = T + 2W + h           // overrun snap fires above this
func (j *V3Buffer) levels(l, h int64) (t, snapTarget, dropLine, overrunAt int64) {
	t = j.floor + l
	snapTarget = t + j.w
	dropLine = t + j.w + h
	overrunAt = t + 2*j.w + h
	return
}

// Write copies src into the ring buffer. Never blocks. src is interleaved
// (length a multiple of NumChannels). Writes longer than capacity are clipped
// to the most-recent capacity frames. The producer never touches reader state.
func (j *V3Buffer) Write(src []float32) {
	nFrames := int64(len(src)) / int64(j.nc)
	if nFrames == 0 {
		return
	}
	if nFrames > j.capacity {
		skip := nFrames - j.capacity
		src = src[skip*int64(j.nc):]
		nFrames = j.capacity
	}
	wp := j.writePos.Load()
	j.writeToRing(src, wp, nFrames)
	// Release: publish writePos *after* the data is in the ring so a consumer
	// that observes the new wp is guaranteed to see the data behind it.
	j.writePos.Store(wp + nFrames)
}

// Read copies up to len(dst) interleaved samples from the ring into dst,
// returning true when audio was produced and false on silence. See
// design_v3.md §4. The window allowances L and H are loaded exactly once at
// the top so every branch sees a consistent geometry. There is no debounce:
// corrections fire on the first out-of-band read.
//
// Phase 2: the window is effectively static (adapt() is a no-op), but the
// per-window correction counters are maintained so Phase 3 only has to consume
// them.
func (j *V3Buffer) Read(dst []float32) bool {
	nc64 := int64(j.nc)
	nFrames := int64(len(dst)) / nc64
	if nFrames == 0 {
		return true
	}
	wp := j.writePos.Load()
	rp := j.readPos.Load()
	fill := wp - rp

	// Load the adaptive window once; derive the operating levels.
	l := j.currentL.Load()
	h := j.currentH.Load()
	_, snapTarget, dropLine, overrunAt := j.levels(l, h)

	// 1. STARTUP — rp == 0 means no Read has yet produced audio. Warm-start:
	// wait for a full operating point (snapTarget) before the first read.
	if rp == 0 {
		if fill < snapTarget {
			clear(dst)
			j.adapt()
			return false
		}
		rp = wp - snapTarget
		j.readPos.Store(rp)
		fill = snapTarget
		// fall through and play this same Read
	}

	// 2. LAP — reader stalled long enough for the writer to wrap.
	if fill >= j.capacity {
		rp = wp - snapTarget
		j.readPos.Store(rp)
		fill = snapTarget
		j.statsLaps.Add(1)
	} else if fill > overrunAt {
		// 3. OVERRUN — sustained drift or a burst above the overrun line.
		rp = wp - snapTarget
		j.readPos.Store(rp)
		fill = snapTarget
		j.statsOverruns.Add(1)
	}

	// 4. UNDERRUN — physical floor: can't satisfy this read. Silence only; rp
	// unchanged. Lap/overrun do not feed adaptation; underrun's only effect is
	// the one-directional insert counter, bumped by the next playing read.
	if fill < nFrames {
		clear(dst)
		j.statsUnderruns.Add(1)
		j.adapt()
		return false
	}

	// 5. PLAYING with optional ±1 splice. Fire on the first out-of-band read;
	// the band is [floor, dropLine] (both include the live L/H via the levels).
	corr := int64(0)
	switch {
	case fill > dropLine && fill >= nFrames+1:
		corr = 1 // DROP
	case fill < j.floor && nFrames >= 2:
		corr = -1 // INSERT
	}

	switch corr {
	case 1:
		// Drop with splice: consume nFrames+1 from the ring, output nFrames.
		// The last output frame is the per-channel average of the last consumed
		// frame and the skipped frame, softening the boundary.
		j.readFromRing(dst, rp, nFrames)
		skipBase := ((rp + nFrames) % j.capacity) * nc64
		dstBase := (nFrames - 1) * nc64
		for ch := int64(0); ch < nc64; ch++ {
			a := dst[dstBase+ch]
			b := j.data[skipBase+ch]
			dst[dstBase+ch] = (a + b) * 0.5
		}
		j.readPos.Store(rp + nFrames + 1)
		j.statsDropped.Add(1)
		j.dropCount++
	case -1:
		// Insert with splice: consume nFrames-1 from the ring, output nFrames.
		// The extra tail frame is the per-channel average of the last consumed
		// frame and the peek-ahead next frame (which stays in the ring).
		realFrames := nFrames - 1
		j.readFromRing(dst[:realFrames*nc64], rp, realFrames)
		peekBase := ((rp + realFrames) % j.capacity) * nc64
		lastBase := (realFrames - 1) * nc64
		tailBase := realFrames * nc64
		for ch := int64(0); ch < nc64; ch++ {
			a := dst[lastBase+ch]
			b := j.data[peekBase+ch]
			dst[tailBase+ch] = (a + b) * 0.5
		}
		j.readPos.Store(rp + realFrames)
		j.statsInserted.Add(1)
		j.insertCount++
	default:
		j.readFromRing(dst, rp, nFrames)
		j.readPos.Store(rp + nFrames)
	}
	j.adapt()
	return true
}

// adapt ticks the tumbling window once per Read and, every windowReads, runs
// the decision off the accumulated correction counts, then resets them. No
// wall-clock: the window is a read count, the inputs are correction counts.
func (j *V3Buffer) adapt() {
	if j.windowReads <= 0 {
		return // no window ⇒ adaptation disabled (constructor always sets 400)
	}
	j.readsThisWindow++
	if j.readsThisWindow < j.windowReads {
		return
	}
	j.lastWinInserts.Store(j.insertCount) // publish for observation before reset
	j.lastWinDrops.Store(j.dropCount)
	j.decide(j.insertCount, j.dropCount)
	j.insertCount = 0
	j.dropCount = 0
	j.readsThisWindow = 0
}

// decide moves the window allowances from one window's correction counts, per
// design_v3.md §6:
//
//   - Both sides breached (min ≥ widenThreshold) ⇒ genuine jitter ⇒ widen the
//     breaching side(s) by widenStep, capped at max. Eager.
//   - Otherwise a fully-calm side (count 0) narrows by narrowStep, floored at
//     min; a side that is lit but un-gated is drift — left to the ±1 corrector.
//
// narrowStep < widenStep makes it eager up / reluctant down, which is the
// stability guarantee (recovery slower than response, errs slightly wide).
func (j *V3Buffer) decide(insertCount, dropCount int64) {
	if min(insertCount, dropCount) >= j.widenThreshold {
		if insertCount >= j.widenThreshold {
			if l := j.currentL.Load(); l < j.lMax {
				j.currentL.Store(min(l+j.widenStep, j.lMax))
			}
		}
		if dropCount >= j.widenThreshold {
			if h := j.currentH.Load(); h < j.hMax {
				j.currentH.Store(min(h+j.widenStep, j.hMax))
			}
		}
		return
	}
	if insertCount == 0 {
		if l := j.currentL.Load(); l > j.lMin {
			j.currentL.Store(max(l-j.narrowStep, j.lMin))
		}
	}
	if dropCount == 0 {
		if h := j.currentH.Load(); h > j.hMin {
			j.currentH.Store(max(h-j.narrowStep, j.hMin))
		}
	}
}

// writeToRing copies nFrames frames from src into the ring at frame position
// wp, handling wraparound. Caller guarantees nFrames <= capacity.
func (j *V3Buffer) writeToRing(src []float32, wp int64, nFrames int64) {
	startFrame := wp % j.capacity
	nc64 := int64(j.nc)
	if startFrame+nFrames <= j.capacity {
		copy(j.data[startFrame*nc64:(startFrame+nFrames)*nc64], src[:nFrames*nc64])
		return
	}
	first := j.capacity - startFrame
	copy(j.data[startFrame*nc64:], src[:first*nc64])
	copy(j.data[:(nFrames-first)*nc64], src[first*nc64:nFrames*nc64])
}

// readFromRing copies nFrames frames from the ring at frame position rp into
// dst, handling wraparound. Caller guarantees nFrames <= capacity.
func (j *V3Buffer) readFromRing(dst []float32, rp int64, nFrames int64) {
	startFrame := rp % j.capacity
	nc64 := int64(j.nc)
	if startFrame+nFrames <= j.capacity {
		copy(dst[:nFrames*nc64], j.data[startFrame*nc64:(startFrame+nFrames)*nc64])
		return
	}
	first := j.capacity - startFrame
	copy(dst[:first*nc64], j.data[startFrame*nc64:])
	copy(dst[first*nc64:nFrames*nc64], j.data[:(nFrames-first)*nc64])
}

// fillFrames returns the current fill in frames. Safe from any goroutine: rp is
// loaded before wp (wp only increases), so the observed fill is non-negative.
func (j *V3Buffer) fillFrames() int64 {
	rp := j.readPos.Load()
	wp := j.writePos.Load()
	return wp - rp
}

// V3Stats is the rich, v3-native snapshot. The asymmetric window is
// exposed as the fixed floor plus the two live allowances L (low/latency) and
// H (high/bunch); the operating target is T = floor + L.
type V3Stats struct {
	FillFrames  int
	FillMs      float64
	FloorFrames int // R + S (fixed)

	LowAllowanceFrames  int // live L
	LowAllowanceMs      float64
	HighAllowanceFrames int // live H
	HighAllowanceMs     float64
	TargetFrames        int // T = floor + L

	Started bool // rp > 0

	Underruns       int64
	Overruns        int64
	Laps            int64
	SamplesDropped  int64
	SamplesInserted int64

	// Most-recent completed tumbling window — shows whether the window is
	// being breached and on which side (the adaptation signal itself).
	LastWindowInserts int64
	LastWindowDrops   int64
}

// Snapshot returns the rich, v3-native stats view. Safe from any goroutine.
func (j *V3Buffer) Snapshot() V3Stats {
	fill := j.fillFrames()
	l := j.currentL.Load()
	h := j.currentH.Load()
	srMs := float64(j.sr) / 1000.0
	return V3Stats{
		FillFrames:          int(fill),
		FillMs:              float64(fill) / srMs,
		FloorFrames:         int(j.floor),
		LowAllowanceFrames:  int(l),
		LowAllowanceMs:      float64(l) / srMs,
		HighAllowanceFrames: int(h),
		HighAllowanceMs:     float64(h) / srMs,
		TargetFrames:        int(j.floor + l),
		Started:             j.readPos.Load() > 0,
		Underruns:           j.statsUnderruns.Load(),
		Overruns:            j.statsOverruns.Load(),
		Laps:                j.statsLaps.Load(),
		SamplesDropped:      j.statsDropped.Load(),
		SamplesInserted:     j.statsInserted.Load(),
		LastWindowInserts:   j.lastWinInserts.Load(),
		LastWindowDrops:     j.lastWinDrops.Load(),
	}
}

// GetBehind returns fill in interleaved floats (matching the ICircularBuffer
// convention).
func (j *V3Buffer) GetBehind() int {
	return int(j.fillFrames()) * j.nc
}
