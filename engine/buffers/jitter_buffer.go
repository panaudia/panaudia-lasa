package buffers

// jitter_buffer.go — the v4 JitterBuffer: envelope sensing + placement
// servo (plan/jitter-v4-design.md; production swap of the jitterlab
// phase-1 core after A1–A9 + regression guards + multi-seed + variant
// comparison all passed). Supersedes the v3 asymmetric-adaptive-window
// buffer, which lives on in jitterlab (V3Buffer) as the measured
// baseline record. The jitterlab golden suite runs against THIS type
// via a thin frames→durations adapter — the acceptance criteria are
// pinned to the shipped code.
//
// Architecture (design §2–§7): a sensing window feeds two fill views —
// the VIRTUAL signal (wp − R·tick, actuation-invariant: the clean view
// of the input process, measured for per-side widths) and RAW fill
// (wp − rp, the physical quantity the servo controls, measured for the
// displacement error and the macro-trim trigger). A P-only servo holds
// the raw-fill window median at a derived setpoint by trickling
// rate-limited ±1 splices through a debt bucket; a confirmed
// whole-window displacement beyond the servo's reach goes in one
// macro-trim. There is no operating band, no drop line, and no
// correction-counting controller — the only discrete events are the two
// physical safety valves (underrun, capacity) and the bounded trim.
//
// Width recurrence (implementation refinement of design §3, recorded in
// the design doc §14): per-window measured widths pass through a rank
// filter — the effective width is the THIRD-highest of the last K
// windows' measurements — before the eager-up/reluctant-down hold. A
// one-off event pollutes at most two windows (it can straddle a window
// boundary), so rank 3 excludes it; anything recurring in ≥3 of K
// windows is learned at full amplitude. This is the ε-quantile lever
// from the prior-art survey (roc's 0.92-of-peaks, NetEq's 95th
// percentile) in its cheapest form, and it is what keeps a one-shot
// ticker catch-up from buying permanent latency while worker-GC clumps
// (every ~2 s) are learned.

import (
	"math"
	"slices"
	"sync/atomic"
	"time"
)

// v4 tuning — one named block (design §12), starting guesses refined
// against the golden suite. No wall-clock values: windows are read
// counts, rates are per second of *read time* (tick · R / sr).
const (
	v4WindowSec    = 2.0 // sensing window length in seconds of read time
	                     // (N = v4WindowSec·sr/R reads: 400 at R = 240,
	                     // 750 at the browser's R = 128 — wall-time
	                     // invariant across geometries, the TS v3 port's
	                     // hand adjustment made structural)
	v4WidthHistory = 10  // K: windows of width history for the rank filter
	v4WidthRank    = 3   // effective width = rank-th highest of the history
	v4FreezeMs     = 40  // no-delivery run that freezes sensing + servo

	v4NarrowStepMs = 0.5 // width decay per window (÷κ) — reluctant down
	v4KLow         = 1.2 // low-side cushion multiplier (·κ)
	v4KHigh        = 1.5 // high-side capacity multiplier (·κ)
	v4WidthLowMax  = 30  // ms; latency-ceiling analog (·κ)
	v4WidthHighMax = 60  // ms (·κ)

	v4DeadbandMs    = 0.25 // servo base deadband (widened to wl/2 under jitter)
	v4GainPerSec    = 0.01 // P anchor gain (frames/s per frame ≈ 0.5 splices/s per ms)
	v4RateCapPerSec = 20   // splice-rate limit (≈ 420 ppm; design review, provisional)
	v4FFClampPerSec = 22   // FF soft-clamp: above the estimator's per-quantum flicker rate (≈ W / triplet span), far below loss-scale slopes
	v4MaxDebt       = 1.5  // debt-bucket clamp
)

// WindowEstimate is one closed sensing window's output, fed to the
// controller (design §9).
type WindowEstimate struct {
	MedianRaw int64 // window median of pre-read raw fill (frames)
	MinRaw    int64 // window min of raw fill — the macro-trim evidence
	MaxRaw    int64
	WidthLow   int64   // effective (rank-filtered, held) late-side width
	WidthHigh  int64   // effective bunch/reader-stall-side width
	SlopeFPS   float64 // virtual-signal slope over the FF horizon, frames/s
	HorizonSec float64 // FF horizon span in seconds of read time
}

// ServoGeometry is the derived geometry a controller decides against.
type ServoGeometry struct {
	Setpoint      int64 // target pre-read fill (frames)
	Deadband      int64 // effective deadband (base, widened by measured width)
	Theta         int64 // macro-trim threshold above setpoint
	GainPerSec    float64
	RateCapPerSec float64
	FFClampPerSec float64
}

// Controller is the per-window decision seam (design §9): everything
// else — sensing, geometry, debt bucket, splices, safety valves — is
// identical across variants. Returns the servo rate for the next window
// (frames/s; positive drops, negative inserts) and an optional
// confirmed macro-trim (frames to discard; 0 = none).
type Controller interface {
	Decide(est WindowEstimate, geo ServoGeometry) (ratePerSec float64, trimFrames int64)
}

// softClamp passes v through unchanged inside ±c and rolls it off as
// c²/|v| beyond — continuous at ±c, → 0 as |v| → ∞. The
// beyond-plausible-drift rejection used by the FF term.
func softClamp(v, c float64) float64 {
	if v > c || v < -c {
		return c * c / v
	}
	return v
}

func clampRate(v, cap_ float64) float64 {
	if v > cap_ {
		return cap_
	}
	if v < -cap_ {
		return -cap_
	}
	return v
}

// PServo is the shipping controller: a drop-only proportional term for
// placement plus a soft-clamped feed-forward term for rate
// reconciliation, and the min-displacement macro-trim (design §5–§6).
type PServo struct{}

//
// Bring-up findings that shaped this law (2026-08-12, recorded in the
// design doc):
//
//   - In W = R geometry the fill LATTICE means drift never moves the
//     fill median — the deficit/surplus arrives as rare whole-packet
//     quantum events. On the HIGH side a surplus quantum persists in
//     the median (nothing removes it), so the P term sees and drains
//     it. On the LOW side the quantum miss punches straight through
//     the underrun valve and the slip erases the evidence (v3's exact
//     A2 pathology) — a P term can never see it. The insert direction
//     therefore rides a FEED-FORWARD term taken from the virtual
//     signal's staircase slope, the one place the sensor lab proved
//     drift-down evidence survives. Over its short horizon the
//     estimator sees one whole packet per quantum event, so the FF
//     repays each quantum as it happens — self-scaling from ±21 ppm
//     (rare 20 s bursts) to ±200 ppm (near-continuous trickle).
//   - The FF also carries drift-UP: once acquired it trickles drops
//     continuously, so at high ppm the quantum steps stop appearing at
//     all (fill stays flat — measured +0.5 ms regret at +200 ppm vs
//     +4.5 with P-only draining each step at the rate cap).
//   - The P term is a SLOW drop-only anchor. Feed-forward alone lets
//     fill wander unboundedly (estimation bias integrates — AOO's
//     documented weakness), so a position term must exist; but a fast
//     P double-pays every quantum the FF is mid-repaying (bring-up
//     measured 1.5× physical splice density at 21 ppm). At ~1 splice/s
//     per ms of displacement the anchor contributes ~20%% during the
//     FF's repay window and converges any wander within a minute. Its
//     real job is the rp-side displacements the virtual signal cannot
//     see — slip offsets under Θ, trim residue, inherited placement.
//   - The FF is SOFT-CLAMPED: f(s) = s for |s| ≤ C, −C²/|s| beyond.
//     Sustained packet loss is indistinguishable from a slow writer in
//     the fill domain (no sequence numbers), but it implies rates far
//     beyond plausible drift — the roll-off sends the FF to ~zero
//     there instead of grinding inserts at the cap, leaving loss to
//     the underrun slips (concealment upstream owns lost audio; v3
//     doctrine kept). Continuous, stateless — no classifier.
func (PServo) Decide(est WindowEstimate, geo ServoGeometry) (float64, int64) {
	if est.MinRaw-geo.Setpoint > geo.Theta {
		// Confirmed offset: even the window MIN stayed displaced. Sized
		// by the median so the trim lands at the setpoint, residue-free.
		return 0, est.MedianRaw - geo.Setpoint
	}
	ff := softClamp(est.SlopeFPS, geo.FFClampPerSec)
	var p float64
	if e := est.MedianRaw - geo.Setpoint; e > geo.Deadband {
		p = geo.GainPerSec * float64(e-geo.Deadband)
	}
	return clampRate(p+ff, geo.RateCapPerSec), 0
}

// QualityLevel maps the LASA stream quality level (0–2, an INTENT —
// "how latency-tolerant is this stream" — declared in the entity
// definition, lasa-core.md §4.2) onto the three continuous per-buffer
// params. The one place the mapping lives; re-tunable without touching
// the protocol. Levels outside 0–2 clamp. The FEC floor is NOT part of
// the level — floors compose via max (FECFloor).
func QualityLevel(level int) (floor time.Duration, robustness float64, capacity time.Duration) {
	switch {
	case level <= 0:
		return 0, 1, 0 // interactive: the zero-value config
	case level == 1:
		return 50 * time.Millisecond, 4, 500 * time.Millisecond // resilient
	default:
		return 150 * time.Millisecond, 8, 400 * time.Millisecond // playback/broadcast
	}
}

// FECFloor derives the jitter-buffer floor a declared LASA redundancy
// intent requires (lasa-core.md §4.2/§5.1): a redundant copy repairs a
// loss only if the buffer already spans the offset, and the floor must
// cover loss CLUSTERS — two overlapping repair holds — not one:
// 2 × (offset + give-up margin of 2 frames) × the wire frame duration
// (A9 bring-up, design doc §14). Zero offset means no FEC, no floor.
func FECFloor(maxOffset int, writerFrame time.Duration) time.Duration {
	if maxOffset <= 0 {
		return 0
	}
	return 2 * time.Duration(maxOffset+2) * writerFrame
}

// JitterBufferConfig configures a JitterBuffer. Zero-valued fields use defaults. The
// per-buffer surface is {Floor, Robustness, Capacity} plus geometry
// (design §8) — no per-route presets; the LASA quality level maps onto
// these via V4QualityLevel.
type JitterBufferConfig struct {
	SampleRate      int
	NumChannels     int
	WriterFrameSize time.Duration // W. No default: the caller declares its wire framing.
	ReaderFrameSize time.Duration // R. No default: the caller declares its callback size.
	Safety          time.Duration // S; default 1 ms

	// WriterFrames/ReaderFrames override the duration fields with exact
	// frame counts when non-zero. Durations cannot express every frame
	// size — the browser's 128-sample worklet quantum is 2.6̄ ms, not a
	// whole number of nanoseconds, and the truncated round-trip (R=127)
	// silently poisons the whole read-count clock (found in TS-port
	// phase 0: the virtual signal ramped 40× the injected drift).
	// Harness and sample-native callers use these; duration callers are
	// unaffected.
	WriterFrames int64
	ReaderFrames int64

	// Floor is the declared minimum operating latency — the LASA
	// quality level's floor and/or the FEC floor (QualityLevel,
	// FECFloor), composed by max with the width-derived setpoint.
	// 0 = none (interactive default).
	Floor time.Duration
	// Robustness is the latency↔robustness knob κ ≥ 1 (0 → 1.0, the
	// low-latency end). It scales responses to MEASURED spread, so it
	// costs nothing on clean networks.
	Robustness float64
	// Capacity is the ring/latency budget; 0 derives the worst case.
	Capacity time.Duration
	// Controller overrides the per-window decision law — the design §9
	// variant seam. Test scaffolding: production always uses the
	// default (nil → PServo).
	Controller Controller
}

// JitterBuffer is the v4 core. Concurrency contract unchanged from v3: one
// producer calls Write (touches only writePos + ring, release-store
// after data), one consumer calls Read (owns everything else); values
// exposed to observers are reader-written atomics.
type JitterBuffer struct {
	// Immutable geometry (frames).
	capacity int64
	w, r, s  int64
	nc       int
	sr       int

	// Immutable derived tuning.
	ctrl        Controller
	windowReads int64
	freezeReads int64
	narrowStep  float64 // frames per window
	spBase      int64   // max(R+S, R+W): the structural lattice floor
	flDecl      int64   // declared floor (FEC lag, deployment min); composes by max
	kLow, kHigh float64
	wlCap       int64
	deadband    int64
	gain        float64
	rateCap     float64
	ffClamp     float64
	ffDeadZone  int64
	theta       int64
	spMax       int64

	// SPSC heads — atomic, cumulative. Ring index = (pos % capacity) * nc.
	writePos atomic.Int64
	readPos  atomic.Int64
	data     []float32

	// Reader-owned sensing/controller state.
	started bool
	tick    int64 // read count since start — the sensing clock
	lastWp  int64
	gapRun  int64
	frozen  bool

	rawBuf  []int64 // pre-read raw fill, current window
	virtBuf []int64 // virtual signal, current window
	sortBuf []int64

	wlHist, whHist []int64 // last K windows' measured widths (rings)
	histLen        int
	histPos        int

	// Feed-forward drift estimator: (tick, window-median virtual) pairs
	// across the last K window closes. Reset on freeze-resume so an
	// outage's virtual step never reads as drift.
	ffTicks []int64
	ffVals  []int64

	wl, wh      float64 // held effective widths (frames)
	rate        float64 // current servo rate (frames/s; + = drop)
	debt        float64
	pendingTrim int64

	// Reader-written atomics for observers. curRate mirrors rate
	// (math.Float64bits) and curFrozen mirrors frozen: the control
	// path uses the plain reader-owned fields; Snapshot must never
	// read those (found by the SPSC race test at the production swap).
	setpoint  atomic.Int64
	curWL     atomic.Int64
	curWH     atomic.Int64
	curRate   atomic.Uint64
	curFrozen atomic.Bool

	statsUnderruns atomic.Int64
	statsLaps      atomic.Int64
	statsOverruns  atomic.Int64
	statsTrims     atomic.Int64
	statsDropped   atomic.Int64
	statsInserted  atomic.Int64
}

// NewJitterBuffer constructs a JitterBuffer. Panics on non-sensical geometry.
func NewJitterBuffer(cfg JitterBufferConfig) *JitterBuffer {
	sr := cfg.SampleRate
	if sr == 0 {
		sr = 48000
	}
	nc := cfg.NumChannels
	if nc == 0 {
		nc = 1
	}
	durToFrames := func(d time.Duration) int64 {
		return int64(d) * int64(sr) / int64(time.Second)
	}
	w, r := cfg.WriterFrames, cfg.ReaderFrames
	if w == 0 {
		w = durToFrames(cfg.WriterFrameSize)
	}
	if r == 0 {
		r = durToFrames(cfg.ReaderFrameSize)
	}
	if w <= 0 || r <= 0 {
		panic("JitterBuffer: writer and reader frame sizes must be > 0")
	}
	s := durToFrames(cfg.Safety)
	if s == 0 {
		s = msToFrames(sr, 1)
	}
	kappa := cfg.Robustness
	if kappa < 1 {
		kappa = 1
	}
	ctrl := cfg.Controller
	if ctrl == nil {
		ctrl = PServo{}
	}

	// The setpoint base includes one whole writer frame of headroom
	// above the read quantum: in W = R geometry drift-down arrives as a
	// single-read whole-packet MISS (the fill lattice — harness finding
	// #1), so a click-free buffer must survive fill dropping by W in one
	// read. R + W is a hard lattice floor, not a tunable cushion. The
	// DECLARED floor (FEC repeat lag, deployment minimum) composes with
	// the width-derived setpoint via MAX, not addition (design §4/§8:
	// "floors compose") — the floor pre-provisions the very lateness the
	// width estimator will later measure, so stacking them would
	// double-provision (found writing the A9 test: floor 30 ms + learned
	// 20 ms FEC-hold width priced 54 ms of latency for a 30 ms need).
	deadband := msToFrames(sr, v4DeadbandMs)
	spBase := max(r+s, r+w)
	flDecl := durToFrames(cfg.Floor)

	kLow := v4KLow * kappa
	kHigh := v4KHigh * kappa
	wlCap := int64(float64(msToFrames(sr, v4WidthLowMax)) * kappa)
	whCap := int64(float64(msToFrames(sr, v4WidthHighMax)) * kappa)
	spMax := max(spBase+int64(kLow*float64(wlCap)), flDecl)

	// The FF dead zone is denominated in the geometry's median
	// quantum: in W = R geometry the fill lattice accumulates drift
	// silently and releases it as whole-W medV steps, so sub-W/2
	// deltas are noise; in W ≠ R geometry there is no lattice — the
	// sawtooth phase glides and the median moves in gcd(W, R) grains
	// (browser 240/128: 16 frames), so a W/2 zone would eat ±21 ppm
	// drift entirely and dump it on the slow anchor (TS-port phase 0
	// finding). gcd/2 is exact in both: 120 at W = R (unchanged), 8 in
	// the browser geometry.
	ffDeadZone := gcd(w, r) / 2
	if ffDeadZone < 1 {
		ffDeadZone = 1
	}

	// Θ is κ-INVARIANT (bring-up finding): scaling it with the knob
	// opens a drip zone between the anchor deadband and the trim
	// threshold where the slow P anchor grinds splices for minutes —
	// the pure-slew failure the trim exists to prevent. A trim removes
	// only stale audio, so it is safe at every robustness level;
	// robust-end trim reluctance, if ever wanted, belongs in the
	// confirmation time, not the threshold height.
	theta := w + int64(v4RateCapPerSec*v4WindowSec)

	capacity := durToFrames(cfg.Capacity)
	if capacity == 0 {
		capacity = 2*(spMax+int64(kHigh*float64(whCap))) + 2*max(w, r)
	}
	// With an explicit (smaller) capacity the setpoint cap must respect
	// it (design §4): leave at least a few frames of clump headroom so a
	// corrupted width estimate can never park fill against the valves.
	if reserve := 4 * max(w, r); spMax > capacity-reserve {
		spMax = max(spBase, capacity-reserve)
	}

	b := &JitterBuffer{
		capacity:    capacity,
		w:           w,
		r:           r,
		s:           s,
		nc:          nc,
		sr:          sr,
		ctrl:        ctrl,
		windowReads: int64(v4WindowSec * float64(sr) / float64(r)),
		freezeReads: max(8, msToFrames(sr, v4FreezeMs)/r),
		narrowStep:  float64(msToFrames(sr, v4NarrowStepMs)) / kappa,
		spBase:      spBase,
		flDecl:      flDecl,
		kLow:        kLow,
		kHigh:       kHigh,
		wlCap:       wlCap,
		deadband:    deadband,
		gain:        v4GainPerSec,
		rateCap:     v4RateCapPerSec,
		ffClamp:     v4FFClampPerSec,
		ffDeadZone:  ffDeadZone,
		theta:       theta,
		spMax:       spMax,
		data:        make([]float32, capacity*int64(nc)),
		wlHist:      make([]int64, v4WidthHistory),
		whHist:      make([]int64, v4WidthHistory),
		ffTicks:     make([]int64, 0, v4WidthHistory),
		ffVals:      make([]int64, 0, v4WidthHistory),
	}
	b.rawBuf = make([]int64, 0, b.windowReads)
	b.virtBuf = make([]int64, 0, b.windowReads)
	b.sortBuf = make([]int64, 0, b.windowReads)
	b.setpoint.Store(min(max(spBase, flDecl), spMax)) // warm start: zero measured width
	return b
}

func msToFrames(sr int, ms float64) int64 {
	return int64(ms * float64(sr) / 1000.0)
}

// Write copies src into the ring. Never blocks; writes larger than
// capacity are clipped to the most-recent frames. Identical to v3.
//
// Concurrency note — the accepted LAP race: in normal operation writer
// and reader touch disjoint ring spans and the release/acquire pair on
// writePos orders them (race-free, verified by the -race SPSC test).
// If the reader stalls beyond ring capacity, the writer overwrites
// unread slots WITHOUT synchronization — formally a data race, visible
// to the race detector, confined to torn float32 audio in a region the
// lap snap then discards. This is deliberate: the alternative
// (dropping writes when full) would freeze wp while the source stream
// advances, and wp's truthfulness is load-bearing — the virtual
// sensing signal (wp − R·tick) and the engine's pose alignment
// (StreamReadPos) both live in the write-stream domain. Overwrite and
// recover; never lie about wp.
func (b *JitterBuffer) Write(src []float32) {
	nFrames := int64(len(src)) / int64(b.nc)
	if nFrames == 0 {
		return
	}
	if nFrames > b.capacity {
		skip := nFrames - b.capacity
		src = src[skip*int64(b.nc):]
		nFrames = b.capacity
	}
	wp := b.writePos.Load()
	b.writeToRing(src, wp, nFrames)
	b.writePos.Store(wp + nFrames) // release: data before position
}

// Read copies up to len(dst) samples from the ring, returning true when
// audio was produced. Order per read: startup gate → pending macro-trim
// → capacity valves → sense → underrun valve → play with debt-bucket
// splice.
func (b *JitterBuffer) Read(dst []float32) bool {
	nc64 := int64(b.nc)
	nFrames := int64(len(dst)) / nc64
	if nFrames == 0 {
		return true
	}
	wp := b.writePos.Load()
	rp := b.readPos.Load()
	sp := b.setpoint.Load()

	// 1. STARTUP — wait for a full operating point, then place exactly at
	// the setpoint (pre-read fill = sp).
	if rp == 0 {
		if wp < sp {
			clear(dst)
			return false
		}
		rp = wp - sp
		b.readPos.Store(rp)
		b.started = true
		b.lastWp = wp // deliveries before start are not a resume event
	}

	// 2. Pending macro-trim (confirmed last window close): one discard,
	// clamped so post-trim fill lands at the setpoint, never below.
	if b.pendingTrim > 0 {
		if t := min(b.pendingTrim, wp-rp-sp); t > 0 {
			rp += t
			b.readPos.Store(rp)
			b.statsTrims.Add(1)
		}
		b.pendingTrim = 0
		b.resetWindow()
	}

	fill := wp - rp

	// 3. Capacity valves: lap (writer wrapped a stalled reader) and the
	// near-capacity guard. Gross events only; snap to the setpoint.
	if fill >= b.capacity {
		rp = wp - sp
		b.readPos.Store(rp)
		fill = sp
		b.statsLaps.Add(1)
		b.resetWindow()
	} else if fill > b.capacity-(b.w+b.r) {
		rp = wp - sp
		b.readPos.Store(rp)
		fill = sp
		b.statsOverruns.Add(1)
		b.resetWindow()
	}

	// 4. Sense at read entry, before the buffer acts (underrun dips are
	// lateness evidence and must be seen; the freeze rule excises
	// sustained absence).
	b.sense(wp, fill)

	// 5. UNDERRUN — physical valve: silence, rp frozen (the free
	// whole-packet time-slip).
	if fill < nFrames {
		clear(dst)
		b.statsUnderruns.Add(1)
		return false
	}

	// 6. PLAY, paying splice debt at most one per read.
	if !b.frozen {
		b.debt += b.rate * float64(b.r) / float64(b.sr)
		if b.debt > v4MaxDebt {
			b.debt = v4MaxDebt
		} else if b.debt < -v4MaxDebt {
			b.debt = -v4MaxDebt
		}
	}
	switch {
	case b.debt >= 1 && fill >= nFrames+1:
		b.debt--
		b.spliceDrop(dst, rp, nFrames)
		b.readPos.Store(rp + nFrames + 1)
		b.statsDropped.Add(1)
	case b.debt <= -1 && nFrames >= 2:
		b.debt++
		b.spliceInsert(dst, rp, nFrames)
		b.readPos.Store(rp + nFrames - 1)
		b.statsInserted.Add(1)
	default:
		b.readFromRing(dst, rp, nFrames)
		b.readPos.Store(rp + nFrames)
	}
	return true
}

// sense feeds one pre-read observation into the sensing window and runs
// the freeze/excise rule (design §3): a no-delivery run longer than
// freezeReads freezes sensing and the servo, and retroactively rewinds
// the run's own dip samples out of the window — outage, loss bursts and
// device stalls teach the estimator nothing. Short dips (a late packet,
// a lost packet) stay in: they are lateness evidence.
func (b *JitterBuffer) sense(wp, fill int64) {
	if !b.started {
		return
	}
	tick := b.tick
	b.tick++

	delta := wp - b.lastWp
	b.lastWp = wp
	if delta > 0 {
		if b.frozen {
			b.frozen = false
			b.curFrozen.Store(false)
			b.resetWindow() // fresh, contiguous-delivery window
			// The frozen span moved the virtual signal by the absence;
			// a slope across it would read as huge drift. Excise.
			b.ffTicks = b.ffTicks[:0]
			b.ffVals = b.ffVals[:0]
		}
		b.gapRun = 0
	} else {
		b.gapRun++
		if !b.frozen && b.gapRun > b.freezeReads {
			b.frozen = true
			b.curFrozen.Store(true)
			b.rate = 0
			b.curRate.Store(0)
			b.debt = 0
			rw := min(b.gapRun-1, int64(len(b.rawBuf)))
			b.rawBuf = b.rawBuf[:int64(len(b.rawBuf))-rw]
			b.virtBuf = b.virtBuf[:int64(len(b.virtBuf))-rw]
		}
	}
	if b.frozen {
		return
	}

	b.rawBuf = append(b.rawBuf, fill)
	b.virtBuf = append(b.virtBuf, wp-b.r*tick)
	if int64(len(b.rawBuf)) >= b.windowReads {
		b.closeWindow()
	}
}

// closeWindow computes the window estimates, updates the width hold,
// the FF drift estimate and the setpoint, and asks the controller for
// the next window's action.
func (b *JitterBuffer) closeWindow() {
	medRaw, minRaw, maxRaw := b.winStats(b.rawBuf)
	medV, minV, maxV := b.winStats(b.virtBuf)

	// Per-side widths from the virtual (actuation-free) view, rank-
	// filtered over K windows, then held eager-up / reluctant-down.
	wlMeas := min(medV-minV, b.wlCap)
	whMeas := maxV - medV
	b.wlHist[b.histPos] = wlMeas
	b.whHist[b.histPos] = whMeas
	b.histPos = (b.histPos + 1) % v4WidthHistory
	if b.histLen < v4WidthHistory {
		b.histLen++
	}
	wlEff := rankNth(b.wlHist[:b.histLen], v4WidthRank)
	whEff := rankNth(b.whHist[:b.histLen], v4WidthRank)
	b.wl = max(float64(wlEff), b.wl-b.narrowStep)
	b.wh = max(float64(whEff), b.wh-b.narrowStep)
	b.curWL.Store(int64(b.wl))
	b.curWH.Store(int64(b.wh))

	// Feed-forward drift estimate: slope of the window-median virtual
	// signal across the K-window horizon, in frames per second of read
	// time. The virtual staircase steps by whole packets at quantum
	// events; over the horizon this averages to the drift rate (one
	// packet repaid per event — self-scaling from rare to continuous).
	b.ffTicks = append(b.ffTicks, b.tick)
	b.ffVals = append(b.ffVals, medV)
	if len(b.ffTicks) > v4WidthHistory {
		b.ffTicks = b.ffTicks[1:]
		b.ffVals = b.ffVals[1:]
	}
	// FF slope: median-of-triplet endpoints over the horizon, with a
	// half-packet dead zone. Raw endpoints chase jitter noise (window
	// medians swing by up to the envelope width — measured as 60–115
	// splices/min of phantom-drift trickle); a full Theil–Sen is robust
	// but a drift staircase's single step is structurally ~half
	// "outlier" to a pairwise median, which broke repayment. The
	// median of each end's three points outvotes any single swung
	// window while a step between the triplets registers fully — and
	// the repay integral stays exact: rate = Δ/span holds for exactly
	// span worth of closes, paying one whole quantum per event. The
	// dead zone drops sub-quantum residue (drift evidence arrives in
	// whole-W steps).
	slope := 0.0
	horizonSec := 0.0
	if n := len(b.ffTicks); n >= 2 && b.ffTicks[n-1] > b.ffTicks[0] {
		lo, hi := b.ffVals[0], b.ffVals[n-1]
		ta, tb := b.ffTicks[0], b.ffTicks[n-1]
		if n >= 6 {
			lo, hi = median3(b.ffVals[0], b.ffVals[1], b.ffVals[2]),
				median3(b.ffVals[n-3], b.ffVals[n-2], b.ffVals[n-1])
			ta, tb = b.ffTicks[1], b.ffTicks[n-2]
		}
		if span := tb - ta; span > 0 {
			perRead := float64(hi-lo) / float64(span)
			if d := perRead * float64(span); d >= float64(b.ffDeadZone) || d <= -float64(b.ffDeadZone) {
				slope = perRead * float64(b.sr) / float64(b.r)
			}
			horizonSec = float64(span) * float64(b.r) / float64(b.sr)
		}
	}

	// Deadband spans the FULL measured late-side envelope: displacement
	// smaller than the jitter envelope is noise, and chasing it would
	// re-commit the v2 sin (design-space, trade axes — multi-seed
	// bring-up measured the half-envelope variant grinding 78–115
	// splices/min draining slip offsets inside the envelope). Under
	// clean networks it stays at the small base so inherited
	// whole-packet offsets are drained promptly; in bursty regimes
	// (deadband > Θ) the anchor cedes entirely to the macro-trim, whose
	// min-window confirmation already outranks the envelope.
	dbEff := max(b.deadband, int64(b.wl))
	sp := min(max(b.spBase+int64(b.kLow*b.wl+0.5), b.flDecl), b.spMax)
	b.setpoint.Store(sp)

	rate, trim := b.ctrl.Decide(
		WindowEstimate{
			MedianRaw: medRaw, MinRaw: minRaw, MaxRaw: maxRaw,
			WidthLow: int64(b.wl), WidthHigh: int64(b.wh),
			SlopeFPS: slope, HorizonSec: horizonSec,
		},
		ServoGeometry{
			Setpoint: sp, Deadband: dbEff, Theta: b.theta,
			GainPerSec: b.gain, RateCapPerSec: b.rateCap,
			FFClampPerSec: b.ffClamp,
		})
	if rate > b.rateCap {
		rate = b.rateCap
	} else if rate < -b.rateCap {
		rate = -b.rateCap
	}
	b.rate = rate
	b.curRate.Store(math.Float64bits(rate))
	if trim > 0 {
		b.pendingTrim = trim
	}
	b.resetWindow()
}

// winStats returns (median, min, max) of a window sample buffer.
func (b *JitterBuffer) winStats(buf []int64) (med, lo, hi int64) {
	b.sortBuf = append(b.sortBuf[:0], buf...)
	slices.Sort(b.sortBuf)
	n := len(b.sortBuf)
	return b.sortBuf[n/2], b.sortBuf[0], b.sortBuf[n-1]
}

func (b *JitterBuffer) resetWindow() {
	b.rawBuf = b.rawBuf[:0]
	b.virtBuf = b.virtBuf[:0]
}

// rankNth returns the n-th highest value — the recurrence filter. A
// one-off event pollutes at most TWO windows (a stall can straddle a
// window boundary — bring-up found exactly this defeating a rank-2
// filter and cascading into a masked offset), so rank 3 excludes any
// single event while genuine recurrence (≥3 of K windows) is learned
// at full amplitude. Short histories use their lowest value (a width
// is only believed once seen n times).
// gcd returns the greatest common divisor of two positive values.
func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// median3 returns the median of three values.
func median3(a, b, c int64) int64 {
	if a > b {
		a, b = b, a
	}
	if b > c {
		b = c
	}
	return max(a, b)
}

func rankNth(h []int64, n int) int64 {
	if len(h) < n {
		n = len(h)
	}
	var top [8]int64 // n ≤ 8; descending
	for i := range n {
		top[i] = -1 << 62
	}
	for _, v := range h {
		for i := range n {
			if v > top[i] {
				copy(top[i+1:n], top[i:n-1])
				top[i] = v
				break
			}
		}
	}
	return top[n-1]
}

// spliceDrop consumes nFrames+1 ring frames into an nFrames output with
// one frame removed at the grade-1 cut: the adjacent consumed pair with
// the minimum summed per-channel discontinuity, averaged into a single
// boundary frame (the decoder's drop marker, at any in-block position).
func (b *JitterBuffer) spliceDrop(dst []float32, rp, nFrames int64) {
	nc64 := int64(b.nc)
	cut := int64(0)
	best := float32(-1)
	for i := int64(0); i < nFrames; i++ {
		d := b.frameDiff(rp+i, rp+i+1)
		if best < 0 || d <= best { // <=: later cut wins ties (v3-shaped tail)
			best = d
			cut = i
		}
	}
	for j := int64(0); j < nFrames; j++ {
		src := rp + j
		if j > cut {
			src++
		}
		sb := (src % b.capacity) * nc64
		db := j * nc64
		if j == cut {
			nb := ((src + 1) % b.capacity) * nc64
			for ch := int64(0); ch < nc64; ch++ {
				dst[db+ch] = (b.data[sb+ch] + b.data[nb+ch]) * 0.5
			}
			continue
		}
		copy(dst[db:db+nc64], b.data[sb:sb+nc64])
	}
}

// spliceInsert consumes nFrames−1 ring frames into an nFrames output
// with one synthetic frame added at the grade-1 cut: the average of the
// adjacent pair around it (the decoder's insert marker).
func (b *JitterBuffer) spliceInsert(dst []float32, rp, nFrames int64) {
	nc64 := int64(b.nc)
	cut := int64(1)
	best := float32(-1)
	for i := int64(1); i < nFrames; i++ {
		d := b.frameDiff(rp+i-1, rp+i)
		if best < 0 || d <= best {
			best = d
			cut = i
		}
	}
	for j := int64(0); j < nFrames; j++ {
		src := rp + j
		if j > cut {
			src--
		}
		db := j * nc64
		if j == cut {
			ab := ((rp + cut - 1) % b.capacity) * nc64
			bb := ((rp + cut) % b.capacity) * nc64
			for ch := int64(0); ch < nc64; ch++ {
				dst[db+ch] = (b.data[ab+ch] + b.data[bb+ch]) * 0.5
			}
			continue
		}
		sb := (src % b.capacity) * nc64
		copy(dst[db:db+nc64], b.data[sb:sb+nc64])
	}
}

// frameDiff is the summed per-channel discontinuity between two frames.
func (b *JitterBuffer) frameDiff(p, q int64) float32 {
	nc64 := int64(b.nc)
	pb := (p % b.capacity) * nc64
	qb := (q % b.capacity) * nc64
	var d float32
	for ch := int64(0); ch < nc64; ch++ {
		x := b.data[pb+ch] - b.data[qb+ch]
		if x < 0 {
			x = -x
		}
		d += x
	}
	return d
}

func (b *JitterBuffer) writeToRing(src []float32, wp, nFrames int64) {
	startFrame := wp % b.capacity
	nc64 := int64(b.nc)
	if startFrame+nFrames <= b.capacity {
		copy(b.data[startFrame*nc64:(startFrame+nFrames)*nc64], src[:nFrames*nc64])
		return
	}
	first := b.capacity - startFrame
	copy(b.data[startFrame*nc64:], src[:first*nc64])
	copy(b.data[:(nFrames-first)*nc64], src[first*nc64:nFrames*nc64])
}

func (b *JitterBuffer) readFromRing(dst []float32, rp, nFrames int64) {
	startFrame := rp % b.capacity
	nc64 := int64(b.nc)
	if startFrame+nFrames <= b.capacity {
		copy(dst[:nFrames*nc64], b.data[startFrame*nc64:(startFrame+nFrames)*nc64])
		return
	}
	first := b.capacity - startFrame
	copy(dst[:first*nc64], b.data[startFrame*nc64:])
	copy(dst[first*nc64:nFrames*nc64], b.data[:(nFrames-first)*nc64])
}

// GetBehind returns fill in interleaved floats (harness fillReporter).
func (b *JitterBuffer) GetBehind() int {
	rp := b.readPos.Load()
	wp := b.writePos.Load()
	return int(wp-rp) * b.nc
}

// JitterBufferStats is the v4 Snapshot: live derived geometry, servo state, and
// event counters. Enough to see what the controller believes and why.
type JitterBufferStats struct {
	FillFrames     int
	SetpointFrames int
	WidthLowFrames int
	WidthHighFrames int
	RatePerSec     float64
	Frozen         bool
	Started        bool

	Underruns       int64
	Laps            int64
	Overruns        int64
	Trims           int64
	SamplesDropped  int64
	SamplesInserted int64
}

// Snapshot returns the stats view. Safe from any goroutine: every
// field read here is a reader-written atomic.
func (b *JitterBuffer) Snapshot() JitterBufferStats {
	rp := b.readPos.Load()
	wp := b.writePos.Load()
	return JitterBufferStats{
		FillFrames:      int(wp - rp),
		SetpointFrames:  int(b.setpoint.Load()),
		WidthLowFrames:  int(b.curWL.Load()),
		WidthHighFrames: int(b.curWH.Load()),
		RatePerSec:      math.Float64frombits(b.curRate.Load()),
		Frozen:          b.curFrozen.Load(),
		Started:         rp > 0,
		Underruns:       b.statsUnderruns.Load(),
		Laps:            b.statsLaps.Load(),
		Overruns:        b.statsOverruns.Load(),
		Trims:           b.statsTrims.Load(),
		SamplesDropped:  b.statsDropped.Load(),
		SamplesInserted: b.statsInserted.Load(),
	}
}
