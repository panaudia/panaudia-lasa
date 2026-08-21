package ambisonic

// M8 silence/decay soak (plan design decision 3 / M8): a scene that goes
// quiet leaves decaying reverb and NFC tails running — on x86 without
// FTZ/DAZ, denormal arithmetic there is 10-100× slower, exactly where no
// steady-signal benchmark looks. The denormal guards (NFC state flush,
// reverb tail flush) must keep the silent-scene frame cost at or below the
// active cost, and must actually engage.
//
// On Apple Silicon denormal float32 math is not pathologically slow, so
// the timing assert here is a logic tripwire; the authoritative timing run
// is the same test on the x86 production box (M8 rollout checklist).

import (
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

func TestSilenceDecaySoak(t *testing.T) {
	if testing.Short() {
		t.Skip("soak is slow")
	}
	setTestDelayModel(t)

	cfg := m1Config()
	cfg.ReverbPreset = common.REVERB_LARGE_HALL // longest tail
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})

	// Near lateral source: NFC biquads active, so their state carries a
	// decaying tail into the silence.
	source := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 1)
	source.SetPosition(common.Position{X: 0.5, Y: 0.55, Z: 0.5})

	mixer, mixerR, reverbMixer := newTestMixers(cfg)
	rng := rand.New(rand.NewSource(6))

	frame := func(silent bool) time.Duration {
		for j := range source.Input {
			if silent {
				source.Input[j] = 0
			} else {
				source.Input[j] = rng.Float32()*2 - 1
			}
		}
		source.PushInputRing()
		start := time.Now()
		listener.EncodePeers([]*Encoder{source}, mixer, mixerR, reverbMixer)
		listener.PostMix()
		return time.Since(start)
	}

	median := func(d []time.Duration) time.Duration {
		s := append([]time.Duration(nil), d...)
		for i := 1; i < len(s); i++ {
			for j := i; j > 0 && s[j] < s[j-1]; j-- {
				s[j], s[j-1] = s[j-1], s[j]
			}
		}
		return s[len(s)/2]
	}

	var active, silent []time.Duration
	for i := 0; i < 200; i++ { // 1 s of signal
		active = append(active, frame(false))
	}
	// 40 s of simulated silence (sub-second wall time): the LARGE_HALL tail
	// (comb feedback ~0.94 over ~1557-sample loops) takes ~22 s of audio to
	// decay below the 1e-18 flush threshold — which itself sits well above
	// the float32 denormal range (~1.2e-38), so the guard engages before
	// denormal arithmetic ever starts.
	for i := 0; i < 8000; i++ {
		silent = append(silent, frame(true))
	}

	actMed, silMed := median(active), median(silent[7000:]) // deep-silence window
	t.Logf("active median %v, deep-silence median %v", actMed, silMed)
	if silMed > actMed*3/2 {
		t.Fatalf("silent-scene frame cost %v exceeds 1.5x active %v — denormal guard failing", silMed, actMed)
	}

	// The guards must have actually engaged by the end of 4 s of silence.
	if !listener.reverb.flushed {
		t.Fatal("reverb tail flush never engaged during sustained silence")
	}
	for ear := 0; ear < 2; ear++ {
		for _, s := range listener.nfcState[source.slot][ear] {
			if s != 0 {
				t.Fatalf("NFC state not flushed to zero after sustained silence: %v", s)
			}
		}
	}
}
