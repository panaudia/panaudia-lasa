package ambisonic

// Parity guarantee for the shared-input reverb path (engine-added,
// P3(a)): a listener rendering the identical scene through the legacy
// per-listener packed reverb and through the shared-matrix dense-masked
// reverb must produce equal output. This is the guard that the
// optimization is acoustically transparent — any divergence is a bug,
// not a tolerance question. (Comparison is by float equality, ==, so
// ±0 differences are tolerated; summation-order differences are not
// expected because zero terms are additive identities.)

import (
	"math"
	"testing"

	"github.com/google/uuid"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

func TestSharedReverbParity(t *testing.T) {
	setTestDelayModel(t)

	cfg := common.MixerConfig{
		MaxNodes:     8,
		FrameSize:    common.FRAME_SIZE,
		ChannelCount: 16,
		Order:        3,
		Size:         1.0,
		ReverbPreset: common.REVERB_MEDIUM_ROOM,
	}

	buildScene := func() (listener *Encoder, sources []*Encoder) {
		listener = NewEncoder(uuid.MustParse("00000000-0000-0000-0000-0000000000aa"), true, 1.0, 2.0, cfg, 0)
		listener.SetPosition(common.Position{X: 1, Y: 1, Z: 1})
		listener.SetRotation(common.Rotation{Yaw: 20})
		for i := 1; i <= 3; i++ {
			s := NewEncoder(uuid.MustParse("00000000-0000-0000-0000-00000000000"+string(rune('0'+i))), true, 1.0, 2.0, cfg, i)
			s.SetPosition(common.Position{X: float64(i) * 1.5, Y: 0.5 * float64(i), Z: 1})
			sources = append(sources, s)
		}
		return
	}

	fillFrame := func(sources []*Encoder, frame int) {
		for si, s := range sources {
			for n := range s.Input {
				s.Input[n] = float32(math.Sin(float64(frame*common.FRAME_SIZE+n)*0.01*float64(si+1))) * 0.5
			}
			s.PushInputRing()
		}
	}

	render := func(shared *SharedInputs) (out, wet []float32) {
		listener, sources := buildScene()
		if shared != nil {
			listener.SetSharedInputs(shared)
		}
		peers := make([]*Encoder, len(sources))
		for i, s := range sources {
			peers[i] = s
		}
		mixer := NewMixer(cfg)
		mixerR := NewMixer(cfg)
		reverbCfg := cfg
		reverbCfg.ChannelCount = common.REVERB_CHANNELS
		reverbCfg.Order = common.OrderForChannelCount(common.REVERB_CHANNELS)
		reverbMixer := NewMixer(reverbCfg)

		// Several frames: exercises the weight crossfade and the
		// double-buffered shared matrices, plus a source position change
		// so current and previous weights genuinely differ.
		for f := 0; f < 4; f++ {
			fillFrame(sources, f)
			if shared != nil {
				for i, s := range sources {
					shared.SetRow(i+1, s.Input)
				}
			}
			if f == 2 {
				sources[0].SetPosition(common.Position{X: 4, Y: 2, Z: 1})
			}
			listener.EncodePeers(peers, mixer, mixerR, reverbMixer)
			listener.PostMix()
		}
		return listener.Output, listener.ReverbOutput
	}

	legacyOut, legacyWet := render(nil)
	sharedOut, sharedWet := render(NewSharedInputs(cfg))

	for i := range legacyWet {
		if !(legacyWet[i] == sharedWet[i]) {
			t.Fatalf("reverb output diverges at %d: legacy %g shared %g",
				i, legacyWet[i], sharedWet[i])
		}
	}
	for i := range legacyOut {
		if !(legacyOut[i] == sharedOut[i]) {
			t.Fatalf("output diverges at %d: legacy %g shared %g",
				i, legacyOut[i], sharedOut[i])
		}
	}
}
