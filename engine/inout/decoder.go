package inout

import (
	"log"

	"gopkg.in/hraban/opus.v2"
)

type InputDecoder interface {
	Decode(src []byte) []float32
}

type BytesInputDecoder struct {
}

func NewBytesInputDecoder() *BytesInputDecoder {
	decoder := BytesInputDecoder{}
	return &decoder
}

func (decoder *BytesInputDecoder) Decode(src []byte) []float32 {
	//common.LogDebug("Decode in BytesInputDecoder")
	return Decodef32(src)
}

// takes opus input data as []byte and returns mono 32bit 48000 in []float32
// supports both stereo input (WebRTC) and mono input (MOQ)
type OpusInputDecoder struct {
	opusDecoder  *opus.Decoder
	stereoBuffer []float32
	monoBuffer   []float32
	channels     int
}

// NewOpusInputDecoder creates a decoder for the given number of input channels.
// channels=1 for mono input (MOQ), channels=2 for stereo input (WebRTC).
func NewOpusInputDecoder(channels int) *OpusInputDecoder {
	decoder := OpusInputDecoder{}

	decoder.channels = channels
	sampleRate := 48000

	d, err := opus.NewDecoder(sampleRate, decoder.channels)
	if err != nil {
		panic(err)
	}
	decoder.opusDecoder = d

	var frameSizeMs float32 = 60 // if you don't know, go with 60 ms.
	frameSize := float32(decoder.channels) * frameSizeMs * float32(sampleRate) / 1000
	if channels == 1 {
		// Mono: decode directly into monoBuffer
		decoder.monoBuffer = make([]float32, int(frameSize))
	} else {
		// Stereo: decode into stereoBuffer, then downmix to monoBuffer
		decoder.stereoBuffer = make([]float32, int(frameSize))
		decoder.monoBuffer = make([]float32, int(frameSize)/2)
	}

	return &decoder
}

func (decoder *OpusInputDecoder) Decode(src []byte) []float32 {
	if decoder.channels == 1 {
		//log.Print("mono")
		// Mono: decode directly to mono buffer
		n, err := decoder.opusDecoder.DecodeFloat32(src, decoder.monoBuffer)
		if err != nil {
			log.Print("decode failed")
			panic(err)
		}
		return decoder.monoBuffer[:n]
	}

	//log.Print("stereo")

	// Stereo: decode then downmix to mono
	n, err := decoder.opusDecoder.DecodeFloat32(src, decoder.stereoBuffer)
	if err != nil {
		log.Print("decode failed")
		panic(err)
	}

	for i := 0; i < n; i++ {
		decoder.monoBuffer[i] = decoder.stereoBuffer[i*2] + decoder.stereoBuffer[(i*2)+1]
	}

	return decoder.monoBuffer[:n]
}
