package inout

import (
	"github.com/panaudia/panaudia-lasa/engine/binaural"
	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// RawF32OutputEncoder is the codec-free binaural sink encoder: the same
// bilateral decode and dynamics as OpusOutputEncoder, then the stereo
// frame handed on as interleaved float32 bytes (1920 bytes per 5 ms
// frame) instead of an Opus packet. It exists so a capacity harness can
// run the composed server pipeline with everything but the codec in it
// (engine.SinkCodecRawF32). It is never a wire format.
type RawF32OutputEncoder struct {
	convolverDecoder        *binaural.ConvolverDecoder
	singleSinkBuffer        *buffers.CBuffer
	ambisonicBufferPointers []uintptr
	dynamics                *StereoCompressorLimiter
}

// NewConvolverRawF32OutputEncoder mirrors NewConvolverOpusOutputEncoder:
// both ear buses contiguously, decoded by the listener's ConvolverDecoder.
// channelCount is per-bus.
func NewConvolverRawF32OutputEncoder(decoder *binaural.ConvolverDecoder, channelCount int) *RawF32OutputEncoder {
	sinkChannels := channelCount * 2
	e := &RawF32OutputEncoder{convolverDecoder: decoder}
	e.singleSinkBuffer = buffers.NewCBuffer(common.FRAME_SIZE * sinkChannels)
	e.ambisonicBufferPointers = make([]uintptr, sinkChannels)
	firstPointer := e.singleSinkBuffer.GetDataPointer()
	for i := 0; i < sinkChannels; i++ {
		e.ambisonicBufferPointers[i] = firstPointer + (uintptr(i) * common.FRAME_SIZE * 4)
	}
	e.dynamics = NewStereoCompressorLimiter(float32(common.SAMPLE_RATE), common.FRAME_SIZE)
	return e
}

// Encode renders one frame to stereo and returns it as float32 bytes.
// The returned slice aliases the decoder's stereo buffer, valid until
// the next call.
func (e *RawF32OutputEncoder) Encode(ambisonicChannels []float32) ([]byte, error) {
	e.singleSinkBuffer.CopyFromSlice(ambisonicChannels)
	e.convolverDecoder.BilateralToStereo(e.ambisonicBufferPointers)
	stereo := e.convolverDecoder.StereoBuffer.AsUnsafeFloatSlice()
	e.dynamics.Process(stereo)
	return Encodef32(stereo), nil
}

func (e *RawF32OutputEncoder) SetRotation(rotation common.Rotation) {}

func (e *RawF32OutputEncoder) BeforeDestroy() {
	e.convolverDecoder.BeforeDestroy()
	e.singleSinkBuffer.BeforeDestroy()
}
