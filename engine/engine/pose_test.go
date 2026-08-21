package engine

import (
	"math"
	"sync"
	"testing"
)

func TestLerpAngleShortestArc(t *testing.T) {
	// Across the ±π wrap: 3.0 → -3.0 is a short hop through π (≈0.283
	// rad), not the long way round through zero.
	mid := lerpAngle(3.0, -3.0, 0.5)
	want := 3.0 + 0.5*(2*math.Pi-6.0)
	if math.Abs(mid-want) > 1e-12 {
		t.Fatalf("wrap lerp: got %v want %v", mid, want)
	}
	if got := lerpAngle(0.1, 0.3, 0.5); math.Abs(got-0.2) > 1e-12 {
		t.Fatalf("plain lerp: got %v", got)
	}
}

func TestPoseRingBracketInterpolation(t *testing.T) {
	var r poseRing
	var scratch [poseRingSize]poseSample
	r.push(1, 100, Pose{X: 0, Yaw: 3.0})
	r.push(2, 200, Pose{X: 10, Yaw: -3.0})

	p, ok := r.poseAt(150, &scratch)
	if !ok {
		t.Fatal("no pose")
	}
	if math.Abs(p.X-5.0) > 1e-12 {
		t.Fatalf("position lerp: got %v", p.X)
	}
	wantYaw := 3.0 + 0.5*(2*math.Pi-6.0)
	if math.Abs(p.Yaw-wantYaw) > 1e-12 {
		t.Fatalf("yaw shortest-arc: got %v want %v", p.Yaw, wantYaw)
	}
}

func TestPoseRingHoldAndStartup(t *testing.T) {
	var r poseRing
	var scratch [poseRingSize]poseSample

	if _, ok := r.poseAt(50, &scratch); ok {
		t.Fatal("empty ring must report no pose")
	}

	r.push(1, 100, Pose{X: 1})
	r.push(2, 200, Pose{X: 2})

	// Past the newest entry: hold it.
	if p, _ := r.poseAt(500, &scratch); p.X != 2 {
		t.Fatalf("hold: got %v", p.X)
	}
	// Before the oldest entry (reader still warming up): earliest known,
	// never extrapolated.
	if p, _ := r.poseAt(50, &scratch); p.X != 1 {
		t.Fatalf("startup: got %v", p.X)
	}
}

func TestPoseRingZeroWidthInterval(t *testing.T) {
	var r poseRing
	var scratch [poseRingSize]poseSample
	// A pose-only packet lands at the same endSample with a higher seq:
	// latest wins across the zero-width interval.
	r.push(1, 100, Pose{X: 1})
	r.push(2, 100, Pose{X: 7})
	r.push(3, 300, Pose{X: 9})
	if p, _ := r.poseAt(100, &scratch); p.X != 7 {
		t.Fatalf("zero-width: got %v", p.X)
	}
}

func TestPoseRingOutOfOrderDropped(t *testing.T) {
	var r poseRing
	var scratch [poseRingSize]poseSample
	r.push(5, 100, Pose{X: 5})
	r.push(4, 200, Pose{X: 4}) // reordered duplicate — dropped
	if p, _ := r.poseAt(500, &scratch); p.X != 5 {
		t.Fatalf("out-of-order pose applied: got %v", p.X)
	}
}

func TestPoseRingWrap(t *testing.T) {
	var r poseRing
	var scratch [poseRingSize]poseSample
	for i := uint64(1); i <= poseRingSize+10; i++ {
		r.push(i, i*100, Pose{X: float64(i)})
	}
	n := r.snapshot(&scratch)
	if n != poseRingSize {
		t.Fatalf("snapshot count: %d", n)
	}
	if scratch[0].seq != poseRingSize+10 {
		t.Fatalf("newest first: %d", scratch[0].seq)
	}
	if p, _ := r.poseAt((poseRingSize+10)*100+5, &scratch); p.X != poseRingSize+10 {
		t.Fatalf("hold newest after wrap: got %v", p.X)
	}
}

func TestPoseRingConcurrent(t *testing.T) {
	// SPSC under -race: one producer, one consumer, checking only that
	// every read is a coherent pose (X == Yaw invariant maintained by
	// the writer).
	var r poseRing
	var wg sync.WaitGroup
	const n = 20000
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); i <= n; i++ {
			v := float64(i)
			r.push(i, i*10, Pose{X: v, Yaw: v})
		}
	}()
	var scratch [poseRingSize]poseSample
	for i := 0; i < 5000; i++ {
		if p, ok := r.poseAt(uint64(i)*40, &scratch); ok {
			if p.X != p.Yaw {
				t.Fatalf("torn pose: X=%v Yaw=%v", p.X, p.Yaw)
			}
		}
	}
	wg.Wait()
}

func TestPoseSlot(t *testing.T) {
	var s poseSlot
	if _, ok := s.load(); ok {
		t.Fatal("empty slot must report no pose")
	}
	s.store(Pose{Yaw: 1})
	s.store(Pose{Yaw: 2})
	p, ok := s.load()
	if !ok || p.Yaw != 2 {
		t.Fatalf("latest-wins: %v %v", p, ok)
	}
}
