package engine

// Head-frame sources (LASA frame:"head", 2026-08-05): the aural HUD —
// pinned relative to every listener's head, verbatim head-relative
// geometry, excluded from world-frame (classic/ambi) renders.

import (
	"math"
	"testing"
)

// headFrameScene: a head-frame tone hard left at 0.5 m, one listener.
func headFrameScene(t *testing.T, listenerPose Pose) (rmsL, rmsR []float64) {
	t.Helper()
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.AddSource("angel", SourceConfig{
		TestTone:    2000,
		InitialPose: Pose{Y: 0.5}, // half a metre off the LEFT ear, head frame
		HeadFrame:   true,
	}); err != nil {
		t.Fatal(err)
	}
	w := &collectingWriter{}
	k, err := m.AddSink("head", w)
	if err != nil {
		t.Fatal(err)
	}
	k.SetPose(listenerPose)
	settle(m, 40)
	return stereoFrames(t, w.frames)
}

// TestHeadFrameInvariance: the angel stays at the listener's left ear
// at the SAME level wherever the listener stands and however they turn
// — the property that defines the feature. (A world source half a
// metre left would flip ears under a π yaw and fade with distance.)
func TestHeadFrameInvariance(t *testing.T) {
	poses := []Pose{
		{},                            // origin, facing +X
		{Yaw: math.Pi},                // about-face
		{X: 20, Y: -7, Yaw: -1.2},     // across the room, turned
		{X: -3, Y: 40, Z: 2, Yaw: 03}, // far away again
	}
	var refL float64
	for i, p := range poses {
		rmsL, rmsR := headFrameScene(t, p)
		n := len(rmsL)
		if n < 30 {
			t.Fatalf("pose %d: only %d frames", i, n)
		}
		var l, r float64
		for j := 20; j < n; j++ {
			l += rmsL[j]
			r += rmsR[j]
		}
		l, r = l/float64(n-20), r/float64(n-20)
		if l == 0 || l < r*1.2 {
			t.Fatalf("pose %d: angel not left-dominant (L=%v R=%v)", i, l, r)
		}
		if i == 0 {
			refL = l
			continue
		}
		if l < refL*0.9 || l > refL*1.1 {
			t.Errorf("pose %d: angel level moved with the listener (L=%v vs ref %v)", i, l, refL)
		}
	}
}

// TestHeadFrameExcludedFromAmbi: the angel never enters a world-frame
// field; a world source on the same entity's render does.
func TestHeadFrameExcludedFromAmbi(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.AddSource("angel", SourceConfig{
		TestTone: 2000, InitialPose: Pose{Y: 0.5}, HeadFrame: true,
	}); err != nil {
		t.Fatal(err)
	}
	bw := &collectingWriter{}
	if _, err := m.AddSink("l", bw); err != nil {
		t.Fatal(err)
	}
	aw := &collectingAmbiWriter{}
	if _, err := m.AddAmbiSink("l", 2, aw); err != nil {
		t.Fatal(err)
	}
	settle(m, 40)

	// Binaural hears the angel; the ambi field does not contain it.
	rmsL, rmsR := stereoFrames(t, bw.frames)
	if rmsL[len(rmsL)-1]+rmsR[len(rmsR)-1] == 0 {
		t.Fatal("binaural must carry the head-frame source")
	}
	aw.mu.Lock()
	ambiFrames := aw.frames
	aw.mu.Unlock()
	rms := channelRMS(t, ambiFrames, 9)
	if rms[0] > 1e-4 {
		t.Fatalf("head-frame source leaked into the world-frame ambi field (W rms %v)", rms[0])
	}

	// A world source appears in both.
	if _, err := m.AddSource("world", SourceConfig{TestTone: 500, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	aw.mu.Lock()
	aw.frames = nil
	aw.mu.Unlock()
	settle(m, 40)
	aw.mu.Lock()
	ambiFrames = aw.frames
	aw.mu.Unlock()
	rms = channelRMS(t, ambiFrames, 9)
	if rms[0] < 1e-3 {
		t.Fatalf("world source missing from the ambi field (W rms %v)", rms[0])
	}
}
