package inout

import (
	"github.com/panaudia/panaudia-lasa/engine/binaural"
	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// BinauralNullOutput runs the full bilateral decode (per-ear SH convolver)
// on each frame and then discards the stereo result. Unlike
// StereoNullOutput — which drops the ambisonic field without decoding — it
// exercises the binaural render path, so it is used as the output for
// performance-test "people" whose audio is never actually sent anywhere.
type BinauralNullOutput struct {
	convolverDecoder        *binaural.ConvolverDecoder
	singleSinkBuffer        *buffers.CBuffer
	ambisonicBufferPointers []uintptr
}

// NewConvolverBinauralNullOutput builds the dual-bus null output: the sink
// carries both ear buses (matching Encoder.Output), decoded by the
// listener's ConvolverDecoder. channelCount is per-bus.
func NewConvolverBinauralNullOutput(decoder *binaural.ConvolverDecoder, channelCount int) *BinauralNullOutput {
	sinkChannels := channelCount * 2
	output := BinauralNullOutput{convolverDecoder: decoder}
	output.singleSinkBuffer = buffers.NewCBuffer(common.FRAME_SIZE * sinkChannels)

	output.ambisonicBufferPointers = make([]uintptr, sinkChannels)
	firstPointer := output.singleSinkBuffer.GetDataPointer()
	for i := 0; i < sinkChannels; i++ {
		output.ambisonicBufferPointers[i] = firstPointer + (uintptr(i) * common.FRAME_SIZE * 4)
	}

	return &output
}

func (output *BinauralNullOutput) WriteAmbisonic(ambisonicChannels []float32) {
	output.singleSinkBuffer.CopyFromSlice(ambisonicChannels)
	// Decode to binaural stereo; the result (decoder StereoBuffer) is left
	// untouched — this is a sink that exists only to spend the CPU.
	output.convolverDecoder.BilateralToStereo(output.ambisonicBufferPointers)
}

func (output *BinauralNullOutput) BeforeDestroy() {
	// Per-output, owns its C handle — destroyed, not released to a pool.
	output.convolverDecoder.BeforeDestroy()
	output.singleSinkBuffer.BeforeDestroy()
}
