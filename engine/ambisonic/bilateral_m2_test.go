package ambisonic

import (
	"math"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// M2/M4 regression anchor with reverb, encoder level, median-plane scene
// (zero ITD → both ears at the integer delayBase, exact-copy fast path);
// the reference side is a CLASSIC listener since M9.4 (the permanent
// raw-output world-frame single mix = the old flag-off path):
//   - channels ≥ 4 (no reverb injection): bus L ≡ bus R, and ≡ the
//     classic stream shifted by delayBase — the dual-bus plumbing anchor.
//   - channels 0–3: bus R's wet signal is velvet-decorrelated (M4 design
//     decision 7), so bus L ≠ bus R there; equal wet ENERGY is asserted
//     (the decorrelator is energy-normalized).
func TestBilateralDualBusReverbAnchors(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	cfg.ReverbPreset = common.REVERB_MEDIUM_ROOM

	listenerOn, peers := m1Scene(t, cfg, 42, 8, true)
	listenerOff := NewClassicEncoder(uuid.New(), false, 1.0, 2.0, cfg, 15)
	listenerOff.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})

	if !listenerOn.DualBus() || len(listenerOn.Output) != 2*listenerOn.BusSamples() {
		t.Fatalf("bilateral encoder is not dual-bus sized: dual=%v len=%d bus=%d",
			listenerOn.DualBus(), len(listenerOn.Output), listenerOn.BusSamples())
	}
	if listenerOff.DualBus() {
		t.Fatal("classic encoder must be single-bus")
	}

	mixer := NewMixer(cfg)
	mixerR := NewMixer(cfg)
	reverbCfg := cfg
	reverbCfg.ChannelCount = common.REVERB_CHANNELS
	reverbMixer := NewMixer(reverbCfg)

	bus := listenerOn.BusSamples()
	frameSize := cfg.FrameSize
	B := int(listenerOn.delayBase)

	// Enough frames for the reverb tail to exist AND settle: the shortest
	// comb delay is 1491 samples (~6.2 frames) and the velvet taps span a
	// further 20 ms, so the coherence gate only accumulates after
	// tailSettleFrames.
	const nFrames = 40
	const tailSettleFrames = 20
	streamOff := make([][]float32, cfg.ChannelCount)
	streamOnL := make([][]float32, cfg.ChannelCount)
	var wetL, wetR, wetDiff float64
	// The wet pair itself (bus L's wet = workBuffer, bus R's = the velvet
	// output): the milestone's coherence gate is on the reverb TAIL, which
	// the shared dry premix would mask in the summed channels.
	var tailL, tailR, tailCross float64

	// Broadband excitation: the wet-tail coherence gate is meaningless on
	// narrowband signals (a tone's post-decorrelator correlation is just
	// cos of some phase offset).
	sig := rand.New(rand.NewSource(4))
	for frame := 0; frame < nFrames; frame++ {
		for _, p := range peers {
			for j := range p.Input {
				p.Input[j] = sig.Float32()*2 - 1
			}
			p.PushInputRing()
		}

		listenerOff.EncodePeers(peers, mixer, mixerR, reverbMixer)
		listenerOff.PostMix()

		listenerOn.EncodePeers(peers, mixer, mixerR, reverbMixer)
		listenerOn.PostMix()

		if frame >= tailSettleFrames {
			for i := 0; i < frameSize; i++ {
				l := float64(listenerOn.reverb.workBuffer[i])
				r := float64(listenerOn.reverb.decorr.out[i])
				tailL += l * l
				tailR += r * r
				tailCross += l * r
			}
		}

		for ch := 0; ch < cfg.ChannelCount; ch++ {
			l := listenerOn.Output[ch*frameSize : (ch+1)*frameSize]
			r := listenerOn.Output[bus+ch*frameSize : bus+(ch+1)*frameSize]
			if ch >= common.REVERB_CHANNELS {
				for i := range l {
					if l[i] != r[i] {
						t.Fatalf("frame %d ch %d sample %d: bus L %v != bus R %v",
							frame, ch, i, l[i], r[i])
					}
				}
			} else {
				for i := range l {
					d := float64(l[i] - r[i])
					wetL += float64(l[i]) * float64(l[i])
					wetR += float64(r[i]) * float64(r[i])
					wetDiff += d * d
				}
			}
			streamOff[ch] = append(streamOff[ch], listenerOff.Output[ch*frameSize:(ch+1)*frameSize]...)
			streamOnL[ch] = append(streamOnL[ch], l...)
		}
	}

	// Dual-bus plumbing anchor on the reverb-free channels: bilateral bus L
	// ≡ classic shifted by delayBase. Frame 0 is skipped: the weight
	// crossfade ramps in output time, the delay shifts input time.
	for ch := common.REVERB_CHANNELS; ch < cfg.ChannelCount; ch++ {
		for n := frameSize + B; n < len(streamOnL[ch]); n++ {
			if streamOnL[ch][n] != streamOff[ch][n-B] {
				t.Fatalf("ch %d sample %d: bilateral %v != classic[%d] %v",
					ch, n, streamOnL[ch][n], n-B, streamOff[ch][n-B])
			}
		}
	}

	// Decorrelation active: the buses' reverb channels genuinely differ,
	// with comparable energy (median-plane dry parts are identical, so the
	// difference energy is bounded by ~2× the wet energy).
	if wetDiff == 0 {
		t.Fatal("bus R reverb channels identical to bus L: decorrelator inactive")
	}
	if ratio := wetL / wetR; ratio < 0.5 || ratio > 2.0 {
		t.Fatalf("bus L/R reverb-channel energy ratio %.2f outside [0.5, 2]", ratio)
	}
	// Interaural coherence of the reverb tail (M4 milestone gate): the
	// velvet decorrelator must hold the wet pair's correlation low (its
	// theoretical floor is ~1/sqrt(taps) ≈ 0.24) at near-equal energy.
	if tailL == 0 || tailR == 0 {
		t.Fatal("wet tail never became nonzero — extend nFrames")
	}
	tailCorr := tailCross / math.Sqrt(tailL*tailR)
	if math.Abs(tailCorr) > 0.5 {
		t.Fatalf("wet-tail interaural correlation %.3f, want |corr| <= 0.5", tailCorr)
	}
	if ratio := tailL / tailR; ratio < 0.5 || ratio > 2.0 {
		t.Fatalf("wet-tail L/R energy ratio %.2f outside [0.5, 2]", ratio)
	}
	t.Logf("wet-tail interaural correlation %.3f, energy ratio %.2f", tailCorr, tailL/tailR)
}

// AddGlobalBuffer must reach both buses (non-reverb path adds the global
// mono W signal directly into Output).
func TestBilateralAddGlobalBufferBothBuses(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config() // REVERB_NONE → global adds straight into Output
	enc := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)

	global := make([]float32, cfg.FrameSize)
	for i := range global {
		global[i] = float32(i%7) * 0.1
	}
	enc.AddGlobalBuffer(global)

	bus := enc.BusSamples()
	for i := range global {
		if enc.Output[i] != global[i] {
			t.Fatalf("bus L sample %d: %v != %v", i, enc.Output[i], global[i])
		}
		if enc.Output[bus+i] != global[i] {
			t.Fatalf("bus R sample %d: %v != %v", i, enc.Output[bus+i], global[i])
		}
	}
}
