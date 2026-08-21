package engine

import (
	"math"
	"sync/atomic"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// Pose is the engine's native pose: metres and radians, x forward, y left,
// z up; positive yaw anticlockwise about z, positive pitch upward, positive
// roll anticlockwise about the line of sight. This matches the LASA wire
// pose conventions, but the engine imports nothing from lasa — protocol
// poses are converted by the caller into this type, and the engine converts
// once, here, into whatever the DSP layer wants.
type Pose struct {
	X, Y, Z          float64 // metres
	Yaw, Pitch, Roll float64 // radians
}

const radToDeg = 180.0 / math.Pi

// position maps to the DSP position type. The engine constructs every
// ambisonic encoder with MixerConfig.Size = 1.0, so the "normalized"
// position IS metres and the copied geometry pass never knows the old 0–1
// representation existed. The single conversion point of the plan's I-D.
func (p Pose) position() common.Position {
	return common.Position{X: p.X, Y: p.Y, Z: p.Z}
}

// rotation maps to the DSP rotation type (degrees; same axis and sign
// conventions — the incumbent client always fed SAF's yaw/pitch/roll).
func (p Pose) rotation() common.Rotation {
	return common.Rotation{Yaw: p.Yaw * radToDeg, Pitch: p.Pitch * radToDeg, Roll: p.Roll * radToDeg}
}

// forward is the pose's facing — local +X in world coordinates — for
// the directivity law. MatrixFromRotation builds the world→local frame
// rotation, so local +X expressed in world is the matrix's first ROW.
// Identity rotation short-circuits (the overwhelmingly common case).
func (p Pose) forward() common.Position {
	if p.Yaw == 0 && p.Pitch == 0 && p.Roll == 0 {
		return common.Position{X: 1}
	}
	m := common.MatrixFromRotation(p.rotation())
	return common.Position{X: m[0], Y: m[1], Z: m[2]}
}

// lerpAngle interpolates radians along the shortest arc; the result may
// leave [-π, π], which no consumer cares about (it feeds sin/cos).
func lerpAngle(a, b, t float64) float64 {
	return a + t*math.Remainder(b-a, 2*math.Pi)
}

func lerpPose(a, b Pose, t float64) Pose {
	return Pose{
		X:     a.X + t*(b.X-a.X),
		Y:     a.Y + t*(b.Y-a.Y),
		Z:     a.Z + t*(b.Z-a.Z),
		Yaw:   lerpAngle(a.Yaw, b.Yaw, t),
		Pitch: lerpAngle(a.Pitch, b.Pitch, t),
		Roll:  lerpAngle(a.Roll, b.Roll, t),
	}
}

// atomicPose stores a Pose as six float64 bit patterns so concurrent
// access is race-detector-clean without locks or allocation (decided over
// a plain-field seqlock, which is correct by fence reasoning but races
// under Go's memory model; and over immutable-snapshot publication, which
// would allocate on the ingest hot path).
type atomicPose struct {
	x, y, z, yaw, pitch, roll atomic.Uint64
}

func (a *atomicPose) store(p Pose) {
	a.x.Store(math.Float64bits(p.X))
	a.y.Store(math.Float64bits(p.Y))
	a.z.Store(math.Float64bits(p.Z))
	a.yaw.Store(math.Float64bits(p.Yaw))
	a.pitch.Store(math.Float64bits(p.Pitch))
	a.roll.Store(math.Float64bits(p.Roll))
}

func (a *atomicPose) load() Pose {
	return Pose{
		X:     math.Float64frombits(a.x.Load()),
		Y:     math.Float64frombits(a.y.Load()),
		Z:     math.Float64frombits(a.z.Load()),
		Yaw:   math.Float64frombits(a.yaw.Load()),
		Pitch: math.Float64frombits(a.pitch.Load()),
		Roll:  math.Float64frombits(a.roll.Load()),
	}
}

// poseRing is the source-pose store: a fixed SPSC ring beside the jitter
// buffer. One writer (the network goroutine, on every pose-bearing packet,
// pose-only packets included), one reader (the mixer tick). Every field is
// atomic, and a per-entry version (odd = write in progress) makes each
// entry's fields mutually consistent: the reader re-checks the version
// after copying and discards the rare torn entry at wrap distance instead
// of locking. Nothing allocates.
//
// Entries pin the pose to endSample — the absolute position in the written
// sample stream at the END of the packet's audio, matching the wire's
// "freshest pose at packet assembly time". The reader evaluates against
// the jitter buffer's StreamReadPos, the same domain: interpolation inside
// the jitter window is free — the audio already paid the delay.
const poseRingSize = 32 // covers any sane decimation at a 200 Hz frame rate

type poseEntry struct {
	version   atomic.Uint64 // even = stable
	seq       atomic.Uint64
	endSample atomic.Uint64
	pose      atomicPose
}

type poseSample struct {
	seq       uint64
	endSample uint64
	pose      Pose
}

type poseRing struct {
	entries [poseRingSize]poseEntry
	head    atomic.Uint64 // entries ever published
	lastSeq uint64        // writer-local: latest-wins guard
}

// push publishes a pose. Writer goroutine only. Anything at or below the
// last written seq is dropped — reordering and redundant duplicates.
func (r *poseRing) push(seq, endSample uint64, pose Pose) {
	h := r.head.Load()
	if h > 0 && seq <= r.lastSeq {
		return
	}
	r.lastSeq = seq
	e := &r.entries[h%poseRingSize]
	v := e.version.Load()
	e.version.Store(v + 1)
	e.seq.Store(seq)
	e.endSample.Store(endSample)
	e.pose.store(pose)
	e.version.Store(v + 2)
	r.head.Store(h + 1)
}

// snapshot copies the most recent entries, newest first, into dst and
// returns the count. Reader goroutine only. A torn entry (the writer
// lapping us at wrap distance) ends the scan — everything older is being
// overwritten anyway.
func (r *poseRing) snapshot(dst *[poseRingSize]poseSample) int {
	h := r.head.Load()
	n := 0
	for i := uint64(0); i < poseRingSize && i < h; i++ {
		e := &r.entries[(h-1-i)%poseRingSize]
		v1 := e.version.Load()
		if v1%2 != 0 {
			break
		}
		s := poseSample{seq: e.seq.Load(), endSample: e.endSample.Load(), pose: e.pose.load()}
		if e.version.Load() != v1 {
			break
		}
		dst[n] = s
		n++
	}
	return n
}

// poseAt evaluates the pose at absolute stream sample L — the last sample
// of the frame being rendered. Bracketing entries are lerped; with no
// later entry the newest at-or-before pose holds; with only later entries
// (startup, before the jitter buffer began playing) the earliest known
// pose is used, never extrapolated. scratch is caller-owned to keep the
// audio thread allocation-free.
func (r *poseRing) poseAt(L uint64, scratch *[poseRingSize]poseSample) (Pose, bool) {
	n := r.snapshot(scratch)
	if n == 0 {
		return Pose{}, false
	}
	var after *poseSample // the oldest entry with endSample > L seen so far
	for i := 0; i < n; i++ {
		e := &scratch[i]
		if e.endSample <= L {
			if after == nil {
				return e.pose, true // newest entry is at-or-before L: hold
			}
			span := after.endSample - e.endSample
			if span == 0 {
				return after.pose, true
			}
			t := float64(L-e.endSample) / float64(span)
			return lerpPose(e.pose, after.pose, t), true
		}
		after = e
	}
	return scratch[n-1].pose, true // everything is ahead of L: earliest known
}

// poseSlot is the sink-pose store: a single latest-wins slot. No
// buffering, no interpolation — for the rotation path staleness is the
// enemy, and a sink has no audio timeline of its own to align to. Written
// by SetPose (one goroutine per sink), read once per tick by the mixer.
// All-atomic fields with a version check, like poseEntry.
type poseSlot struct {
	version atomic.Uint64
	pose    atomicPose
}

func (s *poseSlot) store(p Pose) {
	v := s.version.Load()
	s.version.Store(v + 1)
	s.pose.store(p)
	s.version.Store(v + 2)
}

// load returns the latest pose, false if none was ever stored. Spins only
// across a concurrent store, which is a handful of atomic writes.
func (s *poseSlot) load() (Pose, bool) {
	for {
		v1 := s.version.Load()
		if v1 == 0 {
			return Pose{}, false
		}
		if v1%2 != 0 {
			continue
		}
		p := s.pose.load()
		if s.version.Load() == v1 {
			return p, true
		}
	}
}
