package ambisonic

// M8 per-output render mode (plan design decision 10): raw-ambisonic
// output nodes render CLASSIC — the permanent world-frame single-mix
// contract ROC/Link out depends on (since M9.4 it is the only non-bilateral
// path; the PANAUDIA_BILATERAL flag is gone).

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// BenchmarkEncodePeersSparse measures the plan's M8 memory-bandwidth
// concern: at MaxNodes=128 the four slot-sparse weight buffers cost ~32 KB
// of clear() per listener-frame regardless of how few peers are active.
// If this dominates the sparse case, the optimization is clearing only
// previously-touched slots (measure first — see the M8 status stamp for
// the verdict).
func benchmarkEncodePeersSparse(b *testing.B, maxNodes int) {
	setTestDelayModel(b)

	cfg := m1Config()
	cfg.MaxNodes = maxNodes
	listener, peers := m1Scene(b, cfg, 9, 5, false)
	for _, p := range peers {
		p.PushInputRing()
	}
	mixer, mixerR, reverbMixer := newTestMixers(cfg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		listener.EncodePeers(peers, mixer, mixerR, reverbMixer)
	}
}

// The MaxNodes=128 vs 16 delta isolates the slot-sparse buffer clears
// (three MaxNodes-sized weight buffers cleared per listener-frame however
// few peers are active).
func BenchmarkEncodePeersSparse(b *testing.B)         { benchmarkEncodePeersSparse(b, 128) }
func BenchmarkEncodePeersSparseSmallMax(b *testing.B) { benchmarkEncodePeersSparse(b, 16) }

// A classic encoder with a rotation set and a NEAR lateral source must be
// bit-identical to a classic encoder with no rotation on the same scene:
// the raw-output contract is a world-frame single mix where rotation is
// ignored at encode and none of the bilateral machinery (ITD rings,
// parallax, NFC) leaks in. (Pre-M9.4 this compared classic-flag-on to the
// legacy flag-off path; classic IS that path now, so the anchor pins its
// two invariances directly.)
func TestClassicEncoderIgnoresRotationAndProximity(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	_, peers := m1Scene(t, cfg, 77, 6, false)
	// Include a NEAR lateral source: parallax/NFC must not leak into the
	// classic render.
	peers[0].SetPosition(common.Position{X: 0.5, Y: 0.55, Z: 0.5})

	listenerRot := NewClassicEncoder(uuid.New(), false, 1.0, 2.0, cfg, 14)
	listenerRot.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	// A raw output node must ignore rotation at encode even when set.
	listenerRot.SetRotation(common.Rotation{Yaw: 38, Pitch: -17, Roll: 6})
	listenerPlain := NewClassicEncoder(uuid.New(), false, 1.0, 2.0, cfg, 15)
	listenerPlain.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})

	if listenerRot.DualBus() || len(listenerRot.Output) != listenerRot.BusSamples() {
		t.Fatal("classic encoder must be single-bus")
	}

	mixer, mixerR, reverbMixer := newTestMixers(cfg)

	for frame := 0; frame < 3; frame++ {
		for i, p := range peers {
			for j := range p.Input {
				p.Input[j] = float32(math.Sin(float64(frame*cfg.FrameSize+j)*0.011 + float64(i)))
			}
			p.PushInputRing() // rings always exist; classic render must not read them
		}

		listenerRot.EncodePeers(peers, mixer, mixerR, reverbMixer)
		listenerPlain.EncodePeers(peers, mixer, mixerR, reverbMixer)

		for i := range listenerPlain.Output {
			if listenerRot.Output[i] != listenerPlain.Output[i] {
				t.Fatalf("frame %d sample %d: classic-with-rotation %v != classic %v",
					frame, i, listenerRot.Output[i], listenerPlain.Output[i])
			}
		}
	}
}

// Mixed space: a classic and a bilateral listener render side by side from
// the same sources in the same frame — each pays only for its own path.
func TestMixedRenderModesCoexist(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	classic := NewClassicEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	classic.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	bilateral := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 1)
	bilateral.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})

	source := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 2)
	source.SetPosition(common.Position{X: 0.5, Y: 0.8, Z: 0.5}) // az 90

	mixer, mixerR, reverbMixer := newTestMixers(cfg)
	for frame := 0; frame < 4; frame++ {
		for j := range source.Input {
			source.Input[j] = float32(math.Sin(float64(frame*cfg.FrameSize+j) * 0.02))
		}
		source.PushInputRing()
		classic.EncodePeers([]*Encoder{source}, mixer, mixerR, reverbMixer)
		bilateral.EncodePeers([]*Encoder{source}, mixer, mixerR, reverbMixer)
	}

	if len(classic.Output) != classic.BusSamples() || len(bilateral.Output) != 2*bilateral.BusSamples() {
		t.Fatal("output sizing wrong for mixed modes")
	}
	// The bilateral listener's buses differ (lateral ITD); the classic
	// output is a plain undelayed mix — its W channel must equal the
	// source scaled by W weight, in phase with the input (no delay base).
	bus := bilateral.BusSamples()
	var diff float64
	for i := 0; i < bus; i++ {
		d := float64(bilateral.Output[i] - bilateral.Output[bus+i])
		diff += d * d
	}
	if diff == 0 {
		t.Fatal("bilateral listener's buses identical for a lateral source")
	}
	// Classic W output is proportional to the CURRENT frame's input (no
	// delay): correlation with the input at lag 0 should dominate.
	var num, den1, den2 float64
	for j := 0; j < cfg.FrameSize; j++ {
		w := float64(classic.Output[j])
		x := float64(source.Input[j])
		num += w * x
		den1 += w * w
		den2 += x * x
	}
	if c := num / math.Sqrt(den1*den2); c < 0.999 {
		t.Fatalf("classic W channel not in phase with the source (corr %.4f): delay leaked in", c)
	}
}
