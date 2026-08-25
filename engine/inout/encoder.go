package inout

import (
	"log"
	"unsafe"

	"github.com/panaudia/panaudia-lasa/engine/binaural"
	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/common"
	"gopkg.in/hraban/opus.v2"
)

type OutputEncoder interface {
	Encode(ambisonicChannels []float32) []byte
	SetRotation(rotation common.Rotation)
	// BeforeDestroy releases the encoder's non-GC resources (pool decoder
	// claim, convolver C handle, C sink buffer). Callers must invoke it on
	// the mixer goroutine, after the owning node has stopped rendering.
	BeforeDestroy()
}

type BytesOutputEncoder struct {
	channelCount int
}

func NewBytesOutputEncoder(channelCount int) *BytesOutputEncoder {
	encoder := BytesOutputEncoder{}
	encoder.channelCount = channelCount
	return &encoder
}

func (encoder *BytesOutputEncoder) Encode(ambisonicChannels []float32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&ambisonicChannels[0])), len(ambisonicChannels)*4)
}

func (encoder *BytesOutputEncoder) SetRotation(rotation common.Rotation) {

}

func (encoder *BytesOutputEncoder) BeforeDestroy() {

}

type OpusOutputEncoder struct {
	// ConvolverDecoder is the per-ear SH convolver (M3): it consumes both
	// ear buses in one call. The only binaural decode since M9.4.
	ConvolverDecoder        *binaural.ConvolverDecoder
	opusEncoder             *opus.Encoder
	opusOutputBuffer        []byte
	singleSinkBuffer        *buffers.CBuffer
	AmbisonicBufferPointers []uintptr
	dynamics                *StereoCompressorLimiter
	testTone                *stereoTestTone
}

// NewConvolverOpusOutputEncoder builds the dual-bus encoder: the sink
// carries both 16-ch buses contiguously (bus L then bus R, matching
// Encoder.Output), decoded to stereo by the listener's ConvolverDecoder.
// channelCount is per-bus.
func NewConvolverOpusOutputEncoder(decoder *binaural.ConvolverDecoder, channelCount int) *OpusOutputEncoder {
	outputEncoder := newOpusOutputEncoderBase(channelCount * 2)
	outputEncoder.ConvolverDecoder = decoder
	return outputEncoder
}

func newOpusOutputEncoderBase(sinkChannels int) *OpusOutputEncoder {
	outputEncoder := OpusOutputEncoder{}
	outputEncoder.opusOutputBuffer = make([]byte, 10000)

	outputEncoder.AmbisonicBufferPointers = make([]uintptr, sinkChannels)
	outputEncoder.singleSinkBuffer = buffers.NewCBuffer(common.FRAME_SIZE * sinkChannels)

	firstPointer := outputEncoder.singleSinkBuffer.GetDataPointer()

	for i := 0; i < sinkChannels; i++ {
		outputEncoder.AmbisonicBufferPointers[i] = firstPointer + (uintptr(i) * common.FRAME_SIZE * 4)
	}

	// Coupled stereo: libopus allocates the pair's bitrate jointly
	// (mid/side), which suits a binaural pair better than two fixed
	// mono budgets. No in-band FEC: it is SILK-only and this encoder
	// runs CELT, and loss cover is the LASA redundancy repeat instead.
	opusEncoder, err := opus.NewEncoder(common.SAMPLE_RATE, 2, opus.AppAudio)
	if err != nil {
		panic(err)
	}
	if err := opusEncoder.SetBitrate(BinauralBitrate); err != nil {
		common.LogError("Error setting opus output: %v", err)
	}
	outputEncoder.opusEncoder = opusEncoder
	outputEncoder.dynamics = NewStereoCompressorLimiter(float32(common.SAMPLE_RATE), common.FRAME_SIZE)

	if stereoTestToneEnabled {
		outputEncoder.testTone = newStereoTestTone()
	}

	return &outputEncoder
}

func (outputEncoder *OpusOutputEncoder) BeforeDestroy() {
	// The convolver decoder is per-output and owns its C handle — destroy
	// it outright.
	outputEncoder.ConvolverDecoder.BeforeDestroy()
	outputEncoder.singleSinkBuffer.BeforeDestroy()
}

func (outputEncoder *OpusOutputEncoder) Encode(ambisonicChannels []float32) []byte {

	outputEncoder.singleSinkBuffer.CopyFromSlice(ambisonicChannels)

	// Both ear buses through the per-ear convolver in one call.
	outputEncoder.ConvolverDecoder.BilateralToStereo(outputEncoder.AmbisonicBufferPointers)
	stereoData := outputEncoder.ConvolverDecoder.StereoBuffer.AsUnsafeFloatSlice()

	if outputEncoder.testTone != nil {
		outputEncoder.testTone.Fill(stereoData)
	}

	outputEncoder.dynamics.Process(stereoData)

	nOut, err := outputEncoder.opusEncoder.EncodeFloat32(stereoData,
		outputEncoder.opusOutputBuffer)
	if err != nil {
		log.Print("encode failed")
		panic(err)
	}

	return outputEncoder.opusOutputBuffer[:nOut]
}

func (encoder *OpusOutputEncoder) SetRotation(rotation common.Rotation) {
	// Rotation is applied at encode (head-frame directions,
	// Encoder.SetRotation via the changes queue); the decode is static.
}
