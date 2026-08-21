package engine

import "testing"

// TestProcessAllocs is the hot-path no-allocation guard: after warm-up,
// a full render tick must not allocate. Anything nonzero here is a
// finding to investigate, not a threshold to loosen — the contract is
// "no object allocation on the audio path".
func TestProcessAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation accounting differs under the race detector")
	}

	m, err := New(Config{Order: 3, MaxEntities: 8, ReverbPreset: ReverbMediumRoom, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		if _, err := m.AddSource(id, SourceConfig{TestTone: 200 + float64(i)*10,
			InitialPose: Pose{X: float64(i), Y: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := addNullSink(m, id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		m.Process() // warm-up: buffers grown, kernels dispatched
	}

	avg := testing.AllocsPerRun(200, func() { m.Process() })
	if avg > 0 {
		t.Errorf("Process allocates on the hot path: %.2f allocs/tick", avg)
	}
}
