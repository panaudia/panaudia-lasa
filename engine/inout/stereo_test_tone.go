package inout

import (
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// Diagnostic mode (PANAUDIA_STEREO_TEST): replaces every client's binaural mix
// with hard-panned tones — 440 Hz left only, 880 Hz right only — so any mono
// collapse downstream (decode, graph downmix, OS/Bluetooth) is unambiguous.
// Distinct frequencies per side also make a channel swap audible.

var stereoTestToneEnabled = false

// SetStereoTestTone must be called before any OpusOutputEncoder is created.
func SetStereoTestTone(enabled bool) {
	stereoTestToneEnabled = enabled
}

func StereoTestToneEnabled() bool {
	return stereoTestToneEnabled
}

const (
	stereoTestToneLeftHz  = 440.0
	stereoTestToneRightHz = 880.0
	stereoTestToneLevel   = 0.2
)

type stereoTestTone struct {
	left     *SineMonoInput
	right    *SineMonoInput
	scratchL []float32
	scratchR []float32
}

func newStereoTestTone() *stereoTestTone {
	return &stereoTestTone{
		left:     NewSineMonoInput(stereoTestToneLeftHz, common.SAMPLE_RATE, common.FRAME_SIZE),
		right:    NewSineMonoInput(stereoTestToneRightHz, common.SAMPLE_RATE, common.FRAME_SIZE),
		scratchL: make([]float32, common.FRAME_SIZE),
		scratchR: make([]float32, common.FRAME_SIZE),
	}
}

// Fill overwrites dst (interleaved stereo, LRLR...) with the test tones.
func (tone *stereoTestTone) Fill(dst []float32) {
	frames := len(dst) / 2
	if frames > len(tone.scratchL) {
		frames = len(tone.scratchL)
	}
	tone.left.ReadMono(tone.scratchL[:frames])
	tone.right.ReadMono(tone.scratchR[:frames])
	for i := 0; i < frames; i++ {
		dst[i*2] = tone.scratchL[i] * stereoTestToneLevel
		dst[i*2+1] = tone.scratchR[i] * stereoTestToneLevel
	}
}
