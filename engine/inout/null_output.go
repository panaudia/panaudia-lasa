package inout

import (
	"github.com/panaudia/panaudia-lasa/engine/buffers"
)

type OpusNullOutput struct {
	ambisonicBuffer         *buffers.CBuffer
	opusData                []byte
	encoder                 *OpusOutputEncoder
	AmbisonicBufferPointers []uintptr
}

func NewStereoNullOutput(channelCount int) *OpusNullOutput {
	output := OpusNullOutput{}
	return &output
}

func (output *OpusNullOutput) BeforeDestroy() {
	//output.ambisonicBuffer.BeforeDestroy()
}

func (output *OpusNullOutput) WriteAmbisonic(ambisonicChannels []float32) {
}
