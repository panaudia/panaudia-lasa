package engine

// Order coverage (2026-08-04, Paul): the engine must produce correct
// binaural output at every supported bus order 2–5, through every real
// stage — SH encode at that order, weight pack, GEMM mix, the HRTF
// bank for that order, binaural convolution, opus encode. Order 1 is
// deliberately unsupported.

import (
	"testing"
)

func TestOrderOneRejected(t *testing.T) {
	cfg := testConfig()
	cfg.Order = 1
	if _, err := New(cfg); err == nil {
		t.Fatal("order 1 must be rejected (deliberately unsupported)")
	}
}

// TestBinauralAllOrders: a 2 kHz tone hard left renders at every order
// with real energy and correct lateralisation, and the opus stream
// decodes cleanly — the full binaural path at orders 2–5.
func TestBinauralAllOrders(t *testing.T) {
	for order := 2; order <= 5; order++ {
		t.Run(orderName(order), func(t *testing.T) {
			cfg := testConfig()
			cfg.Order = order
			m, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if _, err := m.AddSource("tone", SourceConfig{TestTone: 2000, InitialPose: Pose{Y: 2}}); err != nil {
				t.Fatal(err)
			}
			w := &collectingWriter{}
			if _, err := m.AddSink("head", w); err != nil {
				t.Fatal(err)
			}
			settle(m, 60)

			rmsL, rmsR := stereoFrames(t, w.frames)
			if len(rmsL) < 40 {
				t.Fatalf("only %d frames rendered", len(rmsL))
			}
			// Judge the settled tail (skip codec/fade warm-up).
			var l, r float64
			for i := 20; i < len(rmsL); i++ {
				l += rmsL[i]
				r += rmsR[i]
			}
			if l == 0 || r == 0 {
				t.Fatalf("order %d: silent output (L=%v R=%v)", order, l, r)
			}
			if l < r*1.2 {
				t.Fatalf("order %d: hard-left source not left-dominant (L=%v R=%v)", order, l, r)
			}
		})
	}
}

func orderName(order int) string {
	return [6]string{"", "", "order2", "order3", "order4", "order5"}[order]
}
