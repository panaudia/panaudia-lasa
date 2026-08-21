package ambisonic

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// TestReverbScratchRace reproduces the reverb spherical-harmonics scratch-buffer
// data race (see ../../cloud-mixer/plan/clustering-crackle/findings.md —
// "Separate latent bug found: reverb SH scratch buffer").
//
// The reverb weight path (GetWeightsForReverb) needs a short-lived scratch slice
// to hold the per-(listener,source) spherical harmonics it writes then reads back
// within one call. The value is LISTENER-relative (norm = listener→source). The
// bug was that EncodePeers handed it peer.sphericalHarmonics — scratch living on
// the *source*, which every listener that hears that source shares. With reverb
// enabled and listeners encoded concurrently on separate render workers, two
// listeners writing the same source's buffer race and corrupt each other's reverb
// weights → continuous artifacts. The fix uses encoder.sphericalHarmonics (the
// listener's own buffer); each listener is encoded by exactly one worker at a
// time, so it is race-free.
//
// This test mirrors the render Across phase: many listeners, all sharing one set
// of source peers, each EncodePeers running in its own goroutine with its own
// dry/reverb mixers (as a real WorkerQueued has). Run under the race detector:
//
//	go test -race -tags=accelerate ./core/ambisonic/ -run TestReverbScratchRace -v
//
// Against the buggy code (peer.sphericalHarmonics) the detector flags a write/write
// + read on the shared source buffer; with the fix it passes clean.
func TestReverbScratchRace(t *testing.T) {
	const (
		numListeners = 8
		numSources   = 6
		ticks        = 20
	)

	mixerConfig := common.MixerConfig{
		MaxNodes:     numSources + numListeners,
		FrameSize:    common.FRAME_SIZE,
		ChannelCount: 9, // 2nd-order ambisonics: (2+1)^2
		Order:        2,
		Size:         2,
		ReverbPreset: common.REVERB_TIGHT_ROOM, // ApplyReverb() == true
	}

	// Reverb mixer runs at REVERB_CHANNELS (mirrors WorkerQueued.NewWorkerQueued).
	reverbMixerConfig := mixerConfig
	reverbMixerConfig.ChannelCount = common.REVERB_CHANNELS
	reverbMixerConfig.Order = common.OrderForChannelCount(common.REVERB_CHANNELS)

	// Shared source peers — the objects every listener reads concurrently.
	sources := make([]*Encoder, numSources)
	for i := range sources {
		src := NewEncoder(uuid.New(), true, 1.0, 2.0, mixerConfig, i)
		src.SetPosition(common.Position{X: float64(i + 1), Y: float64(i + 2), Z: 1})
		for j := range src.Input {
			src.Input[j] = float32(i+1) * 0.01
		}
		sources[i] = src
	}

	// Distinct listeners, each gets its own slot beyond the source range.
	// CLASSIC listeners: GetWeightsForReverb — the scratch mechanism this
	// test exists to race — is the classic reverb path (the bilateral path
	// uses bilateralWeights with per-listener buffers by construction).
	listeners := make([]*Encoder, numListeners)
	for i := range listeners {
		lis := NewClassicEncoder(uuid.New(), false, 1.0, 2.0, mixerConfig, numSources+i)
		lis.SetPosition(common.Position{X: float64(-i - 1), Y: float64(i + 1), Z: 2})
		listeners[i] = lis
	}

	var wg sync.WaitGroup
	for _, lis := range listeners {
		wg.Add(1)
		go func(encoder *Encoder) {
			defer wg.Done()
			// Each goroutine owns its mixers, exactly like one render worker.
			dryMixer := NewMixer(mixerConfig)
			mixerR := NewMixer(mixerConfig)
			reverbMixer := NewMixer(reverbMixerConfig)
			for tick := 0; tick < ticks; tick++ {
				encoder.EncodePeers(sources, dryMixer, mixerR, reverbMixer)
			}
		}(lis)
	}
	wg.Wait()
}
