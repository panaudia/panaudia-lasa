package ambisonic

import (
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// A listener whose audible set empties must emit silence, every frame,
// with reverb on. Regression: ClearOutput used to zero Output only, so
// PostMix kept folding the stale ReverbOutput premix (the last rendered
// frame) back into Output each tick — a 5 ms loop heard as a loud buzz.
func TestEmptyPeersEmitSilenceWithReverb(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	cfg.ReverbPreset = common.REVERB_MEDIUM_ROOM
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	source := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 1)
	source.SetPosition(common.Position{X: 0.8, Y: 0.5, Z: 0.5})

	mixer, mixerR, reverbMixer := newTestMixers(cfg)
	rng := rand.New(rand.NewSource(1))

	// Render a few loud frames so Output and ReverbOutput hold signal.
	for frame := 0; frame < 4; frame++ {
		for j := range source.Input {
			source.Input[j] = rng.Float32()*2 - 1
		}
		source.PushInputRing()
		listener.EncodePeers([]*Encoder{source}, mixer, mixerR, reverbMixer)
		listener.PostMix()
	}
	if peak(listener.Output) == 0 {
		t.Fatal("setup: expected non-zero output while the source is audible")
	}

	// The source leaves: every subsequent emitted frame must be silent,
	// including the first (no transition tracking — the clear is per frame).
	for frame := 0; frame < 8; frame++ {
		listener.EncodePeers(nil, mixer, mixerR, reverbMixer)
		listener.PostMix()
		if p := peak(listener.Output); p != 0 {
			t.Fatalf("frame %d after peers emptied: output peak %g, want exact silence", frame, p)
		}
	}
}

func peak(buf []float32) float32 {
	var p float32
	for _, v := range buf {
		if v < 0 {
			v = -v
		}
		if v > p {
			p = v
		}
	}
	return p
}
