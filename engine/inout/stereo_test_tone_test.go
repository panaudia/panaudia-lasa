package inout

import (
	"math"
	"testing"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

func TestStereoTestToneFill(t *testing.T) {
	tone := newStereoTestTone()

	frames := common.FRAME_SIZE
	dst := make([]float32, frames*2)

	// Two consecutive frames to verify phase continuity across calls
	for frame := 0; frame < 2; frame++ {
		tone.Fill(dst)

		for i := 0; i < frames; i++ {
			n := frame*frames + i
			tSec := float64(n) / float64(common.SAMPLE_RATE)
			wantL := float32(math.Sin(2*math.Pi*stereoTestToneLeftHz*tSec)) * stereoTestToneLevel
			wantR := float32(math.Sin(2*math.Pi*stereoTestToneRightHz*tSec)) * stereoTestToneLevel

			if diff := math.Abs(float64(dst[i*2] - wantL)); diff > 1e-4 {
				t.Fatalf("frame %d sample %d left: got %v want %v", frame, i, dst[i*2], wantL)
			}
			if diff := math.Abs(float64(dst[i*2+1] - wantR)); diff > 1e-4 {
				t.Fatalf("frame %d sample %d right: got %v want %v", frame, i, dst[i*2+1], wantR)
			}
		}
	}
}

func TestStereoTestToneIsHardPanned(t *testing.T) {
	tone := newStereoTestTone()
	dst := make([]float32, common.FRAME_SIZE*2)
	tone.Fill(dst)

	var sumSq, sumDiffSq float64
	for i := 0; i < common.FRAME_SIZE; i++ {
		l := float64(dst[i*2])
		r := float64(dst[i*2+1])
		sumSq += l*l + r*r
		sumDiffSq += (l - r) * (l - r)
	}
	if sumSq == 0 {
		t.Fatal("tone is silent")
	}
	// Mono content would give sumDiffSq == 0; hard-panned distinct tones must not
	if sumDiffSq < sumSq*0.1 {
		t.Fatalf("channels too similar: side energy %v vs total %v", sumDiffSq, sumSq)
	}
}
