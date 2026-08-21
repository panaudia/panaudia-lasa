package buffers

// jitter_buffer_test.go — production-side tests for the v4 JitterBuffer.
// Behaviour is exhaustively covered by the jitterlab acceptance suite
// (A1–A9, knob frontier, FEC floor, variant matrix, multi-seed spreads),
// which runs against this type. Here: the SPSC concurrency contract
// under -race, and the config mapping helpers.

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestJitterBufferSPSCRace exercises the single-producer single-consumer
// contract with a concurrent stats observer — the memory-model claims
// (release-store on writePos after ring data; reader-owned controller
// state; observer-safe atomics) checked under -race.
func TestJitterBufferSPSCRace(t *testing.T) {
	b := NewJitterBuffer(JitterBufferConfig{
		SampleRate:      48000,
		NumChannels:     1,
		WriterFrameSize: 5 * time.Millisecond,
		ReaderFrameSize: 5 * time.Millisecond,
	})
	var stop atomic.Bool
	done := make(chan struct{}, 2)

	go func() { // producer, paced to bounded fill (the SPSC contract:
		// a writer never overwrites unread audio in normal operation;
		// the lap path is a separate recoverable semantic)
		src := make([]float32, 240)
		for !stop.Load() {
			if b.GetBehind() < 2400 { // ≤ 50 ms, far inside capacity
				b.Write(src)
			}
		}
		done <- struct{}{}
	}()
	go func() { // stats observer
		for !stop.Load() {
			_ = b.Snapshot()
			_ = b.GetBehind()
			_ = b.StreamReadPos()
		}
		done <- struct{}{}
	}()

	dst := make([]float32, 240)
	for i := 0; i < 50000; i++ { // consumer: this goroutine
		b.Read(dst)
	}
	stop.Store(true)
	<-done
	<-done
	if st := b.Snapshot(); !st.Started {
		t.Error("buffer never started under concurrent load")
	}
}

func TestQualityLevelMapping(t *testing.T) {
	for _, c := range []struct {
		level int
		floor time.Duration
		rob   float64
	}{
		{-1, 0, 1}, {0, 0, 1},
		{1, 50 * time.Millisecond, 4},
		{2, 150 * time.Millisecond, 8},
		{9, 150 * time.Millisecond, 8}, // clamps
	} {
		floor, rob, _ := QualityLevel(c.level)
		if floor != c.floor || rob != c.rob {
			t.Errorf("QualityLevel(%d) = (%v, %v), want (%v, %v)", c.level, floor, rob, c.floor, c.rob)
		}
	}
}

func TestFECFloorMapping(t *testing.T) {
	w := 5 * time.Millisecond
	if f := FECFloor(0, w); f != 0 {
		t.Errorf("no redundancy must mean no floor, got %v", f)
	}
	// Cluster rule: 2 × (offset + 2 frames of give-up margin) × W.
	if f := FECFloor(3, w); f != 50*time.Millisecond {
		t.Errorf("FECFloor(3) = %v, want 50ms", f)
	}
	if f := FECFloor(7, w); f != 90*time.Millisecond {
		t.Errorf("FECFloor(7) = %v, want 90ms", f)
	}
}
