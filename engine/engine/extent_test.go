package engine

// render.size / render.directivity through the real render
// (plan/size-and-directivity.md, 2026-08-04).

import (
	"math"
	"testing"
)

func setExtent(t *testing.T, m *Mixer, id string, size, directivity float64) {
	t.Helper()
	p := DefaultRenderParams()
	p.Size, p.Directivity = size, directivity
	if err := m.SetRenderParams(id, p); err != nil {
		t.Fatal(err)
	}
}

// TestDirectivityRender: a full-cardioid source facing away from the
// listener is silent; turned to face the listener it renders at its
// omni level.
func TestDirectivityRender(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yaw     float64
		k       float64
		audible bool
	}{
		{"omni facing away", math.Pi, 0, true},
		{"cardioid facing away", 0, 1, false}, // at X:2 facing +X = away from origin
		{"cardioid facing listener", math.Pi, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(testConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if _, err := m.AddSource("s", SourceConfig{TestTone: 300, InitialPose: Pose{X: 2, Yaw: tc.yaw}}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.AddSink("k", &collectingWriter{}); err != nil {
				t.Fatal(err)
			}
			setExtent(t, m, "s", 0, tc.k)
			settle(m, 10)
			e := listenerEnergy(m, "k")
			if tc.audible && e == 0 {
				t.Fatalf("expected audible, got silence")
			}
			if !tc.audible && e > 1e-12 {
				t.Fatalf("expected silence, got energy %v", e)
			}
		})
	}
}

// TestDirectivityHalfSide: k=0.5 with the listener at 90° renders at
// half the omni amplitude — the law reaches the output level.
func TestDirectivityHalfSide(t *testing.T) {
	energyAt := func(k float64) float64 {
		m, err := New(testConfig())
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		// Source left of the listener, facing +X: listener at θ=90°.
		if _, err := m.AddSource("s", SourceConfig{TestTone: 300, InitialPose: Pose{Y: 2}}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.AddSink("k", &collectingWriter{}); err != nil {
			t.Fatal(err)
		}
		setExtent(t, m, "s", 0, k)
		settle(m, 10)
		return listenerEnergy(m, "k")
	}
	ratio := math.Sqrt(energyAt(0.5) / energyAt(0))
	if ratio < 0.45 || ratio > 0.55 {
		t.Fatalf("half-cardioid side amplitude ratio = %v, want ~0.5", ratio)
	}
}

// TestSizeEnvelops: a listener inside a large source still hears it —
// enveloping, not silent and not a gain pump — and a sized source far
// away renders like a point.
func TestSizeEnvelops(t *testing.T) {
	energy := func(size float64) float64 {
		m, err := New(testConfig())
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if _, err := m.AddSource("s", SourceConfig{TestTone: 300, InitialPose: Pose{X: 2}}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.AddSink("k", &collectingWriter{}); err != nil {
			t.Fatal(err)
		}
		setExtent(t, m, "s", size, 0)
		settle(m, 10)
		return listenerEnergy(m, "k")
	}
	point := energy(0)
	inside := energy(8) // surface radius 4, listener at 2 — well inside
	if inside == 0 {
		t.Fatal("inside a large source must be audible (enveloping)")
	}
	if r := inside / point; r < 0.02 || r > 10 {
		t.Fatalf("inside/point energy ratio %v outside sane band", r)
	}
	far := energy(0.1) // 10 cm source at 2 m ≈ point
	if r := math.Sqrt(far / point); r < 0.95 || r > 1.05 {
		t.Fatalf("small far source should render like a point: amplitude ratio %v", r)
	}
}
