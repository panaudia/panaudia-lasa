package jitterlab

import (
	"testing"
	"time"
)

// ---- helpers -------------------------------------------------------------

func v3ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func v3assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic, got none", name)
		}
	}()
	fn()
}

// ---- geometry ------------------------------------------------------------

// All numbers at 48 kHz mono: 1ms = 48 frames. Levels are derived from the
// live (L, H); we check both the warm-start (init) window and the fully-grown
// (L_max, H_max) window. See design_v3.md §3.
func TestV3Geometry_workedExamples(t *testing.T) {
	type lv struct{ T, snap, drop, over int64 }
	cases := []struct {
		name      string
		cfg       V3Config
		floor     int64
		capacity  int64
		atInit    lv
		atMax     lv
	}{
		{
			name: "MOQ 20/5",
			cfg: V3Config{
				WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5),
				// Safety, Low*, High* default: S=1, Linit=5, Lmin=2, Lmax=30, H*=W..3W
			},
			floor: 288, capacity: 14976,
			atInit: lv{528, 1488, 2448, 3408},
			atMax:  lv{1728, 2688, 5568, 6528},
		},
		{
			name: "MOQ 5/5",
			cfg: V3Config{
				WriterFrameSize: v3ms(5), ReaderFrameSize: v3ms(5),
			},
			floor: 288, capacity: 6336,
			atInit: lv{528, 768, 1008, 1248},
			atMax:  lv{1728, 1968, 2688, 2928},
		},
		{
			name: "WebRTC 20/5, LowInit 10",
			cfg: V3Config{
				WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5),
				LowInit: v3ms(10),
			},
			floor: 288, capacity: 14976,
			atInit: lv{768, 1728, 2688, 3648},
			atMax:  lv{1728, 2688, 5568, 6528},
		},
		{
			name: "ROC 10/5, LowInit 10",
			cfg: V3Config{
				WriterFrameSize: v3ms(10), ReaderFrameSize: v3ms(5),
				LowInit: v3ms(10),
			},
			floor: 288, capacity: 9216,
			atInit: lv{768, 1248, 1728, 2208},
			atMax:  lv{1728, 2208, 3648, 4128},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := NewV3Buffer(c.cfg)
			if j.floor != c.floor {
				t.Errorf("floor: want %d, got %d", c.floor, j.floor)
			}
			if j.capacity != c.capacity {
				t.Errorf("capacity: want %d, got %d", c.capacity, j.capacity)
			}
			tI, snI, drI, ovI := j.levels(j.currentL.Load(), j.currentH.Load())
			if (lv{tI, snI, drI, ovI}) != c.atInit {
				t.Errorf("init levels: want %+v, got {%d %d %d %d}", c.atInit, tI, snI, drI, ovI)
			}
			tM, snM, drM, ovM := j.levels(j.lMax, j.hMax)
			if (lv{tM, snM, drM, ovM}) != c.atMax {
				t.Errorf("max levels: want %+v, got {%d %d %d %d}", c.atMax, tM, snM, drM, ovM)
			}

			// Structural invariants for any config / window.
			if !(snI < ovI) {
				t.Errorf("snapTarget (%d) must be < overrunAt (%d)", snI, ovI)
			}
			if !(j.floor+j.w > 0 && snI > j.floor) {
				t.Errorf("snapTarget (%d) must be above floor (%d)", snI, j.floor)
			}
			if !(c.capacity > ovM+j.w) {
				t.Errorf("capacity (%d) must exceed max overrun (%d) + W (%d)", c.capacity, ovM, j.w)
			}
		})
	}
}

// ---- construction / invariants ------------------------------------------

func TestV3Construct_warmStartSeed(t *testing.T) {
	j := NewV3Buffer(V3Config{
		WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5), LowInit: v3ms(5),
	})
	if got := j.currentL.Load(); got != 240 { // 5ms
		t.Errorf("currentL warm start: want 240, got %d", got)
	}
	if got := j.currentH.Load(); got != 960 { // default H_init = W = 20ms
		t.Errorf("currentH warm start: want 960, got %d", got)
	}
}

func TestV3Construct_panics(t *testing.T) {
	v3assertPanics(t, "negative reader frame", func() {
		NewV3Buffer(V3Config{WriterFrameSize: v3ms(20), ReaderFrameSize: -v3ms(1)})
	})
	v3assertPanics(t, "negative writer frame", func() {
		NewV3Buffer(V3Config{WriterFrameSize: -v3ms(1), ReaderFrameSize: v3ms(5)})
	})
	v3assertPanics(t, "low bounds disordered", func() {
		NewV3Buffer(V3Config{
			WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5),
			LowMin: v3ms(40), LowMax: v3ms(30),
		})
	})
	v3assertPanics(t, "high bounds disordered", func() {
		NewV3Buffer(V3Config{
			WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5),
			HighMin: v3ms(100), HighMax: v3ms(30),
		})
	})
}

func TestV3Construct_defaults(t *testing.T) {
	j := NewV3Buffer(V3Config{}) // all defaults
	if j.sr != 48000 || j.nc != 1 {
		t.Errorf("defaults sr/nc: got %d/%d", j.sr, j.nc)
	}
	if j.w != 960 { // 20ms
		t.Errorf("default W: want 960, got %d", j.w)
	}
	if j.floor != 288 { // R(240)+S(48)
		t.Errorf("default floor: want 288, got %d", j.floor)
	}
	if j.lMin != 96 || j.lMax != 1440 {
		t.Errorf("default L bounds: want 96/1440, got %d/%d", j.lMin, j.lMax)
	}
	if j.hMin != 960 || j.hMax != 2880 { // W and 3W
		t.Errorf("default H bounds: want 960/2880, got %d/%d", j.hMin, j.hMax)
	}
	// Controller constants surfaced (Phase 3 consumes them).
	if j.windowReads != 400 || j.widenThreshold != 5 || j.widenStep != 96 || j.narrowStep != 24 {
		t.Errorf("controller consts: got N=%d thr=%d widen=%d narrow=%d",
			j.windowReads, j.widenThreshold, j.widenStep, j.narrowStep)
	}
}

// ---- Write ---------------------------------------------------------------

func TestV3Write_fillAccounting(t *testing.T) {
	j := NewV3Buffer(V3Config{WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5)})
	j.Write(make([]float32, 100))
	if got := j.fillFrames(); got != 100 {
		t.Errorf("fill after write 100: want 100, got %d", got)
	}
	j.Write(make([]float32, 50))
	if got := j.fillFrames(); got != 150 {
		t.Errorf("fill after write +50: want 150, got %d", got)
	}
}

func TestV3Write_wrap(t *testing.T) {
	j := NewV3Buffer(V3Config{WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5)})
	// Position the write head two frames before the ring wrap.
	start := j.capacity - 2
	j.writePos.Store(start)
	src := []float32{1, 2, 3, 4, 5}
	j.writeToRing(src, start, int64(len(src)))
	// Read the same span back; the wrap must be transparent.
	dst := make([]float32, 5)
	j.readFromRing(dst, start, 5)
	for i := range src {
		if dst[i] != src[i] {
			t.Fatalf("wrap round-trip mismatch at %d: want %v, got %v", i, src, dst)
		}
	}
}

// ---- Read path (Phase 2) -------------------------------------------------

// v3buf builds a buffer with explicit small geometry for branch/splice tests,
// bypassing the constructor. With floor=20, w=10, l=10, h=10 the levels are
// T=30, snapTarget=40, dropLine=50, overrunAt=60, band [floor 20 .. dropLine 50].
func v3buf(capacity, nc int, floor, w, l, h int64) *V3Buffer {
	j := &V3Buffer{
		capacity: int64(capacity),
		floor:    floor,
		w:        w,
		nc:       nc,
		sr:       48000,
		data:     make([]float32, capacity*nc),
	}
	j.currentL.Store(l)
	j.currentH.Store(h)
	return j
}

func v3indexRing(j *V3Buffer) { // mono: ring[i] = i
	for i := range j.data {
		j.data[i] = float32(i)
	}
}

func v3eq(t *testing.T, name string, got, want []float32) {
	t.Helper()
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: dst[%d] want %v, got %v (full %v)", name, i, want[i], got[i], got)
			return
		}
	}
}

func TestV3Read_startupSilence(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10) // snapTarget = 40
	j.Write(make([]float32, 30))       // fill 30 < 40
	dst := []float32{9, 9, 9, 9}
	if j.Read(dst) {
		t.Fatal("startup with fill<snapTarget should return false")
	}
	if j.readPos.Load() != 0 {
		t.Errorf("rp must stay 0, got %d", j.readPos.Load())
	}
	v3eq(t, "silence", dst, []float32{0, 0, 0, 0})
}

func TestV3Read_startupSnapAndPlay(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10) // snapTarget = 40
	v3indexRing(j)
	j.writePos.Store(100) // fill 100 >= 40; ring[i]=i
	dst := make([]float32, 4)
	if !j.Read(dst) {
		t.Fatal("startup with fill>=snapTarget should play")
	}
	// snap rp = 100-40 = 60; fill 40 in band -> plain read ring[60..64]; rp 64
	if j.readPos.Load() != 64 {
		t.Errorf("rp: want 64, got %d", j.readPos.Load())
	}
	v3eq(t, "startup play", dst, []float32{60, 61, 62, 63})
}

func TestV3Read_lapSnaps(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10)
	v3indexRing(j)
	j.writePos.Store(300)
	j.readPos.Store(50) // fill 250 >= capacity 200, rp != 0
	dst := make([]float32, 4)
	if !j.Read(dst) {
		t.Fatal("lap should snap and play")
	}
	if j.statsLaps.Load() != 1 {
		t.Errorf("laps: want 1, got %d", j.statsLaps.Load())
	}
	// snap rp = 300-40 = 260; play ring[260%200=60 ..]; rp 264
	if j.readPos.Load() != 264 {
		t.Errorf("rp: want 264, got %d", j.readPos.Load())
	}
	v3eq(t, "lap play", dst, []float32{60, 61, 62, 63})
}

func TestV3Read_overrunSnaps(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10) // overrunAt = 60
	v3indexRing(j)
	j.writePos.Store(100)
	j.readPos.Store(30) // fill 70 > 60, < capacity, rp != 0
	dst := make([]float32, 4)
	if !j.Read(dst) {
		t.Fatal("overrun should snap and play")
	}
	if j.statsOverruns.Load() != 1 {
		t.Errorf("overruns: want 1, got %d", j.statsOverruns.Load())
	}
	if j.readPos.Load() != 64 { // snap rp = 100-40 = 60; play; rp 64
		t.Errorf("rp: want 64, got %d", j.readPos.Load())
	}
	v3eq(t, "overrun play", dst, []float32{60, 61, 62, 63})
}

func TestV3Read_underrunSilences(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10)
	j.writePos.Store(102)
	j.readPos.Store(100) // fill 2 < nFrames 4, rp != 0
	dst := []float32{9, 9, 9, 9}
	if j.Read(dst) {
		t.Fatal("underrun should return false")
	}
	if j.statsUnderruns.Load() != 1 {
		t.Errorf("underruns: want 1, got %d", j.statsUnderruns.Load())
	}
	if j.readPos.Load() != 100 {
		t.Errorf("rp must not advance, got %d", j.readPos.Load())
	}
	v3eq(t, "underrun silence", dst, []float32{0, 0, 0, 0})
}

func TestV3Read_playingNoCorrection(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10) // band [20, 50]
	v3indexRing(j)
	j.writePos.Store(130)
	j.readPos.Store(100) // fill 30, in band
	dst := make([]float32, 4)
	if !j.Read(dst) {
		t.Fatal("in-band read should play")
	}
	if j.readPos.Load() != 104 {
		t.Errorf("rp: want 104, got %d", j.readPos.Load())
	}
	if j.statsInserted.Load() != 0 || j.statsDropped.Load() != 0 {
		t.Errorf("no correction expected: ins=%d drop=%d", j.statsInserted.Load(), j.statsDropped.Load())
	}
	v3eq(t, "in-band play", dst, []float32{100, 101, 102, 103})
}

func TestV3Read_insertSplice(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10) // floor 20
	j.data[100], j.data[101], j.data[102], j.data[103] = 10, 20, 30, 40
	j.writePos.Store(115)
	j.readPos.Store(100) // fill 15 < floor 20, >= nFrames
	dst := make([]float32, 4)
	if !j.Read(dst) {
		t.Fatal("insert path should play")
	}
	// consume 3 (ring[100..103]); tail = (ring[102]=30 + peek ring[103]=40)/2 = 35
	v3eq(t, "insert", dst, []float32{10, 20, 30, 35})
	if j.readPos.Load() != 103 { // advance by realFrames = 3
		t.Errorf("rp: want 103, got %d", j.readPos.Load())
	}
	if j.statsInserted.Load() != 1 || j.insertCount != 1 {
		t.Errorf("insert counters: stats=%d window=%d", j.statsInserted.Load(), j.insertCount)
	}
}

func TestV3Read_dropSplice(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10) // dropLine 50, overrunAt 60
	j.data[100], j.data[101], j.data[102], j.data[103], j.data[104] = 10, 20, 30, 40, 50
	j.writePos.Store(160)
	j.readPos.Store(100) // fill 60 > dropLine 50, == overrunAt (not >), so drop not overrun
	dst := make([]float32, 4)
	if !j.Read(dst) {
		t.Fatal("drop path should play")
	}
	// consume 5 (ring[100..104]); last out = (ring[103]=40 + skipped ring[104]=50)/2 = 45
	v3eq(t, "drop", dst, []float32{10, 20, 30, 45})
	if j.readPos.Load() != 105 { // advance by nFrames+1 = 5
		t.Errorf("rp: want 105, got %d", j.readPos.Load())
	}
	if j.statsDropped.Load() != 1 || j.dropCount != 1 {
		t.Errorf("drop counters: stats=%d window=%d", j.statsDropped.Load(), j.dropCount)
	}
}

func TestV3Read_dropSpliceWrap(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10)
	j.data[198], j.data[199], j.data[0], j.data[1], j.data[2] = 1, 2, 3, 4, 5
	j.writePos.Store(258)
	j.readPos.Store(198) // fill 60 -> drop; read wraps
	dst := make([]float32, 4)
	if !j.Read(dst) {
		t.Fatal("drop-across-wrap should play")
	}
	// ring[198,199,0,1] = 1,2,3,4; tail = (4 + skipped ring[2]=5)/2 = 4.5
	v3eq(t, "drop wrap", dst, []float32{1, 2, 3, 4.5})
	if j.readPos.Load() != 203 {
		t.Errorf("rp: want 203, got %d", j.readPos.Load())
	}
}

func TestV3Read_nFrames1PrecludesInsert(t *testing.T) {
	j := v3buf(200, 1, 20, 10, 10, 10)
	j.data[100] = 7
	j.writePos.Store(110)
	j.readPos.Store(100) // fill 10 < floor 20, but nFrames=1
	dst := make([]float32, 1)
	if !j.Read(dst) {
		t.Fatal("nFrames=1 below floor should plain-read, not underrun")
	}
	if j.insertCount != 0 || j.statsInserted.Load() != 0 {
		t.Error("nFrames=1 must preclude insert")
	}
	v3eq(t, "nFrames1", dst, []float32{7})
	if j.readPos.Load() != 101 {
		t.Errorf("rp: want 101, got %d", j.readPos.Load())
	}
}

func TestV3Read_insertSpliceStereo(t *testing.T) {
	j := v3buf(200, 2, 20, 10, 10, 10) // nc=2
	set := func(frame int64, l, r float32) { j.data[frame*2] = l; j.data[frame*2+1] = r }
	set(100, 10, 11)
	set(101, 20, 21)
	set(102, 30, 31)
	set(103, 40, 41) // peek
	j.writePos.Store(115)
	j.readPos.Store(100) // fill 15 < floor 20
	dst := make([]float32, 8) // 4 stereo frames
	if !j.Read(dst) {
		t.Fatal("stereo insert should play")
	}
	// frames 100,101,102 then tail = avg(frame102, frame103) per channel = (35, 36)
	v3eq(t, "stereo insert", dst, []float32{10, 11, 20, 21, 30, 31, 35, 36})
	if j.readPos.Load() != 103 {
		t.Errorf("rp: want 103, got %d", j.readPos.Load())
	}
}

// End-to-end with real constructor geometry: a balanced 20ms-write / 5ms-read
// stream (W = 4R) rides the natural sawtooth entirely inside the band, so after
// startup there must be zero corrections, snaps, and underruns.
func TestV3Read_steadyStreamClean(t *testing.T) {
	j := NewV3Buffer(V3Config{WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5)})
	const W, R = 960, 240
	j.Write(make([]float32, W)) // prime past snapTarget (1488); 2x960 = 1920
	j.Write(make([]float32, W))
	read := make([]float32, R)
	for i := 0; i < 4000; i++ {
		if i%4 == 0 { // one writer frame per four reads keeps the rate balanced
			j.Write(make([]float32, W))
		}
		j.Read(read)
	}
	if u := j.statsUnderruns.Load(); u != 0 {
		t.Errorf("steady stream underruns: want 0, got %d", u)
	}
	if ins, drop := j.statsInserted.Load(), j.statsDropped.Load(); ins != 0 || drop != 0 {
		t.Errorf("steady stream corrections: ins=%d drop=%d, want 0", ins, drop)
	}
	if ov, lap := j.statsOverruns.Load(), j.statsLaps.Load(); ov != 0 || lap != 0 {
		t.Errorf("steady stream snaps: overrun=%d lap=%d, want 0", ov, lap)
	}
}

func TestV3Write_overCapacityClip(t *testing.T) {
	j := NewV3Buffer(V3Config{WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5)})
	cap := j.capacity
	src := make([]float32, cap+10)
	for i := range src {
		src[i] = float32(i)
	}
	j.Write(src)
	if got := j.fillFrames(); got != cap {
		t.Fatalf("fill after over-capacity write: want %d, got %d", cap, got)
	}
	// Clip keeps the LAST `cap` frames: ring[0] = src[10], ring[cap-1] = src[cap+9].
	dst := make([]float32, cap)
	j.readFromRing(dst, 0, cap)
	if dst[0] != 10 {
		t.Errorf("clipped first frame: want 10, got %v", dst[0])
	}
	if dst[cap-1] != float32(cap+9) {
		t.Errorf("clipped last frame: want %d, got %v", cap+9, dst[cap-1])
	}
}

// ---- Adaptation: decide() unit tests (Phase 3) ---------------------------

// No ring needed; decide() only touches currentL/currentH and the bounds.
func v3adaptBuf(l, h int64) *V3Buffer {
	j := &V3Buffer{
		sr:             48000,
		lMin:           10,
		lMax:           100,
		hMin:           10,
		hMax:           100,
		windowReads:    20,
		widenThreshold: 5,
		widenStep:      8,
		narrowStep:     2,
	}
	j.currentL.Store(l)
	j.currentH.Store(h)
	return j
}

func v3wantLH(t *testing.T, name string, j *V3Buffer, wantL, wantH int64) {
	t.Helper()
	if l, h := j.currentL.Load(), j.currentH.Load(); l != wantL || h != wantH {
		t.Errorf("%s: want L=%d H=%d, got L=%d H=%d", name, wantL, wantH, l, h)
	}
}

func TestV3Decide_bothDirectionsWidens(t *testing.T) {
	j := v3adaptBuf(20, 20)
	j.decide(5, 5) // both >= threshold 5
	v3wantLH(t, "both widen", j, 28, 28)
}

func TestV3Decide_asymmetricStillWidensBoth(t *testing.T) {
	j := v3adaptBuf(20, 20)
	j.decide(20, 5) // both >= 5, unequal
	v3wantLH(t, "asymmetric widen", j, 28, 28)
}

func TestV3Decide_widenCapsAtMax(t *testing.T) {
	j := v3adaptBuf(96, 96)
	j.decide(10, 10) // 96+8 = 104 -> cap 100
	v3wantLH(t, "cap at max", j, 100, 100)
}

func TestV3Decide_driftDownHoldsLNarrowsH(t *testing.T) {
	j := v3adaptBuf(50, 50)
	j.decide(10, 0) // inserts only: not jitter. L lit-but-ungated (held); H calm (narrows)
	v3wantLH(t, "drift down", j, 50, 48)
}

func TestV3Decide_driftUpHoldsHNarrowsL(t *testing.T) {
	j := v3adaptBuf(50, 50)
	j.decide(0, 10) // drops only
	v3wantLH(t, "drift up", j, 48, 50)
}

func TestV3Decide_calmNarrowsToMinNotBelow(t *testing.T) {
	j := v3adaptBuf(12, 12)
	j.decide(0, 0)
	v3wantLH(t, "calm narrow", j, 10, 10) // 12-2 = 10 = min
	j.decide(0, 0)
	v3wantLH(t, "calm floored", j, 10, 10) // stays at min
}

func TestV3Decide_incidentalOppositeDoesNotTrip(t *testing.T) {
	j := v3adaptBuf(50, 50)
	j.decide(10, 2) // mostly drift-down with a couple incidental drops; min 2 < 5
	v3wantLH(t, "incidental", j, 50, 50) // no widen; H not narrowed (drop != 0)
}

func TestV3Adapt_tumblingResets(t *testing.T) {
	j := v3adaptBuf(20, 20)
	j.windowReads = 4
	j.insertCount, j.dropCount = 5, 5
	for i := 0; i < 3; i++ {
		j.adapt() // reads 1-3, below N: no decide
	}
	if j.currentL.Load() != 20 {
		t.Errorf("decide fired before window full: L=%d", j.currentL.Load())
	}
	if j.readsThisWindow != 3 {
		t.Errorf("readsThisWindow: want 3, got %d", j.readsThisWindow)
	}
	j.adapt() // read 4 == N: decide(5,5) widens, then reset
	v3wantLH(t, "after window", j, 28, 28)
	if j.insertCount != 0 || j.dropCount != 0 || j.readsThisWindow != 0 {
		t.Errorf("not reset: ins=%d drop=%d rtw=%d", j.insertCount, j.dropCount, j.readsThisWindow)
	}
}

// ---- Adaptation: forced-fill integration (Phase 3) -----------------------

// v3simBuf has a ring plus adaptation tuning. levels(40,40): floor 100, T 140,
// snapTarget 340, dropLine 380, overrunAt 580.
func v3simBuf() *V3Buffer {
	j := &V3Buffer{
		capacity:       4000,
		floor:          100,
		w:              200,
		nc:             1,
		sr:             48000,
		lMin:           20,
		lMax:           200,
		hMin:           20,
		hMax:           200,
		windowReads:    10,
		widenThreshold: 3,
		widenStep:      16,
		narrowStep:     4,
		data:           make([]float32, 4000),
	}
	j.currentL.Store(40)
	j.currentH.Store(40)
	return j
}

// v3force scripts one Read at a chosen fill (rp anchored nonzero so the startup
// branch is skipped), exercising the correction the fill implies.
//
//	fill 80  -> insert (< floor 100)
//	fill 400 -> drop   (> dropLine 380, <= overrunAt 580)
//	fill 250 -> in band, no correction
//	fill 2   -> underrun (< nFrames 4)
func v3force(j *V3Buffer, fill int64) {
	rp := j.capacity // % capacity == 0 and != 0
	j.readPos.Store(rp)
	j.writePos.Store(rp + fill)
	j.Read(make([]float32, 4))
}

func TestV3Adapt_driftDownDoesNotWidenL(t *testing.T) {
	j := v3simBuf()
	for i := 0; i < 30; i++ { // inserts only, 3 windows
		v3force(j, 80)
	}
	if l := j.currentL.Load(); l != 40 {
		t.Errorf("drift-down must not widen L: want 40, got %d", l)
	}
	if h := j.currentH.Load(); h >= 40 {
		t.Errorf("calm high side should narrow: got %d (init 40)", h)
	}
}

func TestV3Adapt_driftUpDoesNotWidenH(t *testing.T) {
	j := v3simBuf()
	for i := 0; i < 30; i++ { // drops only
		v3force(j, 400)
	}
	if h := j.currentH.Load(); h != 40 {
		t.Errorf("drift-up must not widen H: want 40, got %d", h)
	}
	if l := j.currentL.Load(); l >= 40 {
		t.Errorf("calm low side should narrow: got %d (init 40)", l)
	}
}

func TestV3Adapt_jitterWidensBoth(t *testing.T) {
	j := v3simBuf()
	for i := 0; i < 20; i++ { // alternate insert/drop: both lit each window
		if i%2 == 0 {
			v3force(j, 80)
		} else {
			v3force(j, 400)
		}
	}
	if l := j.currentL.Load(); l <= 40 {
		t.Errorf("jitter should widen L: got %d (init 40)", l)
	}
	if h := j.currentH.Load(); h <= 40 {
		t.Errorf("jitter should widen H: got %d (init 40)", h)
	}
}

// The v2 ratchet bug, gone: a sustained outage (drain then underruns) is
// one-directional, so L never grows toward max.
func TestV3Adapt_outageDoesNotRatchetL(t *testing.T) {
	j := v3simBuf()
	for i := 0; i < 5; i++ { // draining: inserts
		v3force(j, 80)
	}
	for i := 0; i < 50; i++ { // drained: underruns (feed no counter)
		v3force(j, 2)
	}
	l := j.currentL.Load()
	if l > 40 {
		t.Errorf("outage must not grow L (v2 ratchet bug): got %d, init 40, max %d", l, j.lMax)
	}
	if l < j.lMin {
		t.Errorf("L narrowed below min: %d", l)
	}
}

func TestV3Adapt_jitterThenCalmNarrowsBack(t *testing.T) {
	j := v3simBuf()
	for i := 0; i < 40; i++ { // jitter -> grow
		if i%2 == 0 {
			v3force(j, 80)
		} else {
			v3force(j, 400)
		}
	}
	grown := j.currentL.Load()
	if grown <= 40 {
		t.Fatalf("jitter should have grown L: got %d", grown)
	}
	for i := 0; i < 300; i++ { // long calm in-band -> narrow back
		v3force(j, 250)
	}
	calm := j.currentL.Load()
	if calm >= grown {
		t.Errorf("calm should narrow L back: grown=%d, calm=%d", grown, calm)
	}
	if calm < j.lMin {
		t.Errorf("narrowed below min: %d", calm)
	}
}

// ---- Adaptation: real clock-driven jitter sim (Phase 3) ------------------

// Drives a real-geometry buffer with a balanced write schedule whose arrivals
// alternate 6ms late / 6ms early — genuinely two-sided jitter (lateness > R, so
// it isn't swallowed by per-read batching). Both sides should widen, the
// allowances stay bounded by their caps, and the widened window keeps the
// stream clean (no underruns).
func TestV3Sim_steadyJitterBoundedGrowth(t *testing.T) {
	j := NewV3Buffer(V3Config{
		WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5),
		LowInit: v3ms(1), LowMin: v3ms(1), LowMax: v3ms(15),
		HighInit: v3ms(1), HighMin: v3ms(1), HighMax: v3ms(15),
	})
	const W, R, lead, X = 960, 240, 960, 288 // X = 6ms, alternating late/early
	jit := func(k int) int {
		if k%2 == 0 {
			return X // late
		}
		return -X // early
	}
	sched := func(k int) int { return k*W - lead + jit(k) }
	now, nextW := 0, 0
	for r := 0; r < 8000; r++ {
		now += R
		for sched(nextW) <= now {
			j.Write(make([]float32, W))
			nextW++
		}
		j.Read(make([]float32, R))
	}
	l, h := j.currentL.Load(), j.currentH.Load()
	if l > j.lMax || h > j.hMax { // safety: never beyond the cap
		t.Errorf("allowance exceeded max: L=%d (max %d), H=%d (max %d)", l, j.lMax, h, j.hMax)
	}
	if l <= 48 || h <= 48 { // 48 frames = 1ms init: two-sided jitter widens both
		t.Errorf("expected both sides to widen under two-sided jitter: L=%d H=%d (init 48)", l, h)
	}
	if u := j.statsUnderruns.Load(); u >= 10 {
		t.Errorf("widened window should keep the stream clean: underruns=%d", u)
	}
}

// ---- Stats & observation (Phase 4) ---------------------------------------

func TestV3Snapshot_fields(t *testing.T) {
	j := NewV3Buffer(V3Config{
		WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5), LowInit: v3ms(5),
	})
	s := j.Snapshot()
	if s.FloorFrames != 288 {
		t.Errorf("FloorFrames: want 288, got %d", s.FloorFrames)
	}
	if s.LowAllowanceFrames != 240 || s.LowAllowanceMs != 5.0 {
		t.Errorf("Low: want 240/5.0, got %d/%v", s.LowAllowanceFrames, s.LowAllowanceMs)
	}
	if s.HighAllowanceFrames != 960 || s.HighAllowanceMs != 20.0 {
		t.Errorf("High: want 960/20.0, got %d/%v", s.HighAllowanceFrames, s.HighAllowanceMs)
	}
	if s.TargetFrames != 528 { // floor 288 + L 240
		t.Errorf("Target: want 528, got %d", s.TargetFrames)
	}
	if s.Started || s.FillFrames != 0 {
		t.Errorf("fresh buffer: Started=%v Fill=%d", s.Started, s.FillFrames)
	}
}

func TestV3Snapshot_afterActivity(t *testing.T) {
	j := NewV3Buffer(V3Config{WriterFrameSize: v3ms(20), ReaderFrameSize: v3ms(5)})
	for i := 0; i < 3; i++ {
		j.Write(make([]float32, 960))
	}
	j.Read(make([]float32, 240)) // fill 2880 >= snapTarget 1488: starts and plays
	s := j.Snapshot()
	if !s.Started {
		t.Error("Started should be true after a successful Read")
	}
	if s.FillFrames <= 0 {
		t.Errorf("FillFrames should be > 0, got %d", s.FillFrames)
	}
}



func TestV3GetBehind(t *testing.T) {
	j := v3buf(200, 2, 20, 10, 10, 10) // stereo
	j.readPos.Store(100)
	j.writePos.Store(110) // 10 frames
	if gb := j.GetBehind(); gb != 20 { // 10 frames * 2 channels
		t.Errorf("GetBehind: want 20, got %d", gb)
	}
}

func TestV3Snapshot_lastWindowCounts(t *testing.T) {
	j := v3simBuf() // windowReads = 10
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			v3force(j, 80) // insert
		} else {
			v3force(j, 400) // drop
		}
	}
	s := j.Snapshot()
	if s.LastWindowInserts != 5 || s.LastWindowDrops != 5 {
		t.Errorf("last-window counts: want 5/5, got %d/%d", s.LastWindowInserts, s.LastWindowDrops)
	}
}
