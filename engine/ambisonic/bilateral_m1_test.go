package ambisonic

import (
	"math"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

func m1Config() common.MixerConfig {
	return common.MixerConfig{
		MaxNodes:     16,
		FrameSize:    common.FRAME_SIZE,
		ChannelCount: 16,
		Order:        3,
		Size:         10,
		ReverbPreset: common.REVERB_NONE,
	}
}

// newTestMixers mirrors space.NewWorkerQueued's mixer construction: the
// reverb mixer runs at REVERB_CHANNELS, not the full channel count. (A
// full-width reverb mixer with reverb enabled makes Mix write past
// ReverbOutput — silent heap corruption under the old C path, a shape
// panic since M9.2.)
func newTestMixers(cfg common.MixerConfig) (mixer, mixerR, reverbMixer *Mixer) {
	reverbCfg := cfg
	reverbCfg.ChannelCount = common.REVERB_CHANNELS
	reverbCfg.Order = common.OrderForChannelCount(common.REVERB_CHANNELS)
	return NewMixer(cfg), NewMixer(cfg), NewMixer(reverbCfg)
}

// setTestDelayModel installs the Woodworth parameters flag-on encoder
// construction requires (production wires them from the HRTF manifest).
func setTestDelayModel(t testing.TB) {
	prevA, prevC := common.BilateralHeadRadiusM, common.BilateralSpeedOfSoundMS
	common.BilateralHeadRadiusM = 0.0875
	common.BilateralSpeedOfSoundMS = 343.0
	t.Cleanup(func() {
		common.BilateralHeadRadiusM, common.BilateralSpeedOfSoundMS = prevA, prevC
	})
}

// m1Scene builds a listener + sources. medianPlane pins every source to the
// listener's Y (zero lateral angle → zero Woodworth ITD) AND at far-field
// distance (≥ ParallaxFarFieldM, so both ears share one weight vector) —
// the degenerate configuration that keeps the flag-on/flag-off anchors
// bit-exact from M4/M5 on. Near or lateral sources genuinely differ per
// ear by design (ITD, mirror-symmetric parallax weights).
func m1Scene(t testing.TB, cfg common.MixerConfig, seed int64, nSources int, medianPlane bool) (*Encoder, []*Encoder) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))

	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})

	peers := make([]*Encoder, nSources)
	for i := range peers {
		peers[i] = NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, i+1)
		pos := common.Position{X: rng.Float64(), Y: rng.Float64(), Z: rng.Float64()}
		if medianPlane {
			pos.Y = 0.5
			for {
				dx, dz := (pos.X-0.5)*cfg.Size, (pos.Z-0.5)*cfg.Size
				if dx*dx+dz*dz >= (ParallaxFarFieldM+0.5)*(ParallaxFarFieldM+0.5) {
					break
				}
				pos.X, pos.Z = rng.Float64(), rng.Float64()
			}
		}
		peers[i].SetPosition(pos)
		for j := range peers[i].Input {
			peers[i].Input[j] = rng.Float32()*2 - 1
		}
	}
	return listener, peers
}

// M1/M4 regression anchor (reworked at M9.4, the flag deleted): a
// bilateral listener with identity rotation and median-plane sources
// (zero ITD) must be bit-identical to a CLASSIC listener — the permanent
// raw-output world-frame single mix, i.e. the old flag-off path — shifted
// by the constant delayBase the encode ITD stage adds (integer, so the
// pack takes the exact-copy fast path). Both buses must be bit-identical
// to each other. Classic reads peer Input, bilateral reads the rings, so
// one scene serves both listeners in the same frame.
func TestBilateralIdentityMedianPlaneShiftedBitExactVsClassic(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	listenerOn, peers := m1Scene(t, cfg, 42, 8, true)
	listenerOn.SetRotation(common.Rotation{}) // identity
	listenerOff := NewClassicEncoder(uuid.New(), false, 1.0, 2.0, cfg, 15)
	listenerOff.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})

	mixer, mixerR, reverbMixer := newTestMixers(cfg)

	const nFrames = 4
	frameSize := cfg.FrameSize
	bus := listenerOn.BusSamples()
	B := int(listenerOn.delayBase)
	streamOff := make([][]float32, cfg.ChannelCount)
	streamOn := make([][]float32, cfg.ChannelCount)

	for frame := 0; frame < nFrames; frame++ {
		for i, p := range peers {
			for j := range p.Input {
				p.Input[j] = float32(math.Sin(float64(frame*frameSize+j)*0.01 + float64(i)))
			}
			p.PushInputRing()
		}

		listenerOff.EncodePeers(peers, mixer, mixerR, reverbMixer)
		listenerOn.EncodePeers(peers, mixer, mixerR, reverbMixer)

		for i := 0; i < bus; i++ {
			if listenerOn.Output[i] != listenerOn.Output[bus+i] {
				t.Fatalf("frame %d sample %d: bus L %v != bus R %v (median plane)",
					frame, i, listenerOn.Output[i], listenerOn.Output[bus+i])
			}
		}
		for ch := 0; ch < cfg.ChannelCount; ch++ {
			streamOff[ch] = append(streamOff[ch], listenerOff.Output[ch*frameSize:(ch+1)*frameSize]...)
			streamOn[ch] = append(streamOn[ch], listenerOn.Output[ch*frameSize:(ch+1)*frameSize]...)
		}
	}

	// Skip frame 0: the weight crossfade ramps in OUTPUT time while the
	// delay shifts INPUT time, so the fade-in frame can't match under a
	// shift. Positions are static, so every later frame has constant
	// weights and the equality is exact.
	for ch := range streamOn {
		for n := frameSize + B; n < len(streamOn[ch]); n++ {
			if streamOn[ch][n] != streamOff[ch][n-B] {
				t.Fatalf("ch %d sample %d: flag-on %v != flag-off[%d] %v (shift %d)",
					ch, n, streamOn[ch][n], n-B, streamOff[ch][n-B], B)
			}
		}
	}
}

// Head-frame weights for a source at world direction v must equal
// world-frame weights for a source physically placed at direction R·v at
// the same distance (re-encoding at rotated directions is exact for point
// sources; only float rounding differs between rotate-then-normalize and
// normalize-then-rotate).
func TestBilateralRotatedWeightsMatchRotatedSource(t *testing.T) {
	cfg := m1Config()
	listenerM := common.Position{X: 5, Y: 5, Z: 5}
	peerM := common.Position{X: 7.2, Y: 4.1, Z: 5.9}

	rot := common.Rotation{Yaw: 38, Pitch: -17, Roll: 64}
	m := common.MatrixFromRotation(rot)

	headWeights := make([]float32, cfg.ChannelCount)
	GetWeights(headWeights, 1.0, 1.0, listenerM, peerM, &m, cfg.Order, common.Position{X: 1}, 0, 0)

	// Physically rotate the source about the listener instead.
	rel := common.TrigCartesianRelativePosition(listenerM, peerM)
	relRot := m.Apply(rel)
	rotatedPeerM := common.TrigCartesianSumPosition(listenerM, relRot)
	worldWeights := make([]float32, cfg.ChannelCount)
	GetWeights(worldWeights, 1.0, 1.0, listenerM, rotatedPeerM, nil, cfg.Order, common.Position{X: 1}, 0, 0)

	for i := range headWeights {
		diff := math.Abs(float64(headWeights[i] - worldWeights[i]))
		if diff > 1e-6 {
			t.Fatalf("weight[%d]: head-frame %v vs rotated-source %v (diff %v)",
				i, headWeights[i], worldWeights[i], diff)
		}
	}
}

func benchmarkEncodePeers(b *testing.B, classic bool, rotation common.Rotation) {
	setTestDelayModel(b)

	cfg := m1Config()
	listener, peers := m1Scene(b, cfg, 7, cfg.MaxNodes-1, false)
	if classic {
		listener = NewClassicEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
		listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	}
	listener.SetRotation(rotation)
	for _, p := range peers {
		p.PushInputRing()
	}
	mixer, mixerR, reverbMixer := newTestMixers(cfg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		listener.EncodePeers(peers, mixer, mixerR, reverbMixer)
	}
}

// The classic (raw-output world-frame single mix) encode is the cost
// baseline the bilateral ratio is tracked against — it IS the old
// flag-off path (M9.4).
func BenchmarkEncodePeersClassic(b *testing.B) {
	benchmarkEncodePeers(b, true, common.Rotation{})
}

func BenchmarkEncodePeersBilateralRotated(b *testing.B) {
	benchmarkEncodePeers(b, false, common.Rotation{Yaw: 38, Pitch: -17, Roll: 6})
}
