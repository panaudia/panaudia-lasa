package inout

// Opus multistream codec bindings for the ambisonic sink formats
// (lasa audio-packet-structure.md §7): N uncoupled mono streams,
// identity mapping — ambi2 = 9, ambi3 = 16, ACN/SN3D handled by the
// caller. hraban/opus does not expose the multistream API, so these
// are direct cgo bindings over the same libopus we already link.

/*
#cgo pkg-config: opus
#include <opus_multistream.h>
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// OpusMSEncoder encodes interleaved float32 multichannel frames into
// one Opus multistream packet per call. Single-goroutine use (the
// render Out phase); Encode reuses an internal buffer valid until the
// next call.
type OpusMSEncoder struct {
	enc      *C.OpusMSEncoder
	channels int
	buf      []byte
}

// NewOpusMSEncoder creates an all-uncoupled multistream encoder:
// `channels` streams, coupled = 0, identity mapping, OPUS_APPLICATION_AUDIO.
func NewOpusMSEncoder(channels int) (*OpusMSEncoder, error) {
	mapping := make([]C.uchar, channels)
	for i := range mapping {
		mapping[i] = C.uchar(i)
	}
	var cerr C.int
	enc := C.opus_multistream_encoder_create(
		C.opus_int32(common.SAMPLE_RATE),
		C.int(channels),
		C.int(channels), // streams
		0,               // coupled_streams
		&mapping[0],
		C.OPUS_APPLICATION_AUDIO,
		&cerr,
	)
	if cerr != C.OPUS_OK || enc == nil {
		return nil, fmt.Errorf("inout: opus_multistream_encoder_create: %d", int(cerr))
	}
	return &OpusMSEncoder{
		enc:      enc,
		channels: channels,
		buf:      make([]byte, channels*1500),
	}, nil
}

// Encode encodes one frame of interleaved samples (len = frameSize ×
// channels) and returns the multistream packet, valid until the next
// call. Allocation-free after construction.
func (e *OpusMSEncoder) Encode(interleaved []float32) ([]byte, error) {
	frames := len(interleaved) / e.channels
	n := C.opus_multistream_encode_float(
		e.enc,
		(*C.float)(unsafe.Pointer(&interleaved[0])),
		C.int(frames),
		(*C.uchar)(unsafe.Pointer(&e.buf[0])),
		C.opus_int32(len(e.buf)),
	)
	if n < 0 {
		return nil, fmt.Errorf("inout: opus_multistream_encode_float: %d", int(n))
	}
	return e.buf[:int(n)], nil
}

// BeforeDestroy releases the C encoder. Audio thread, after the owning
// sink has stopped rendering (the funnel lesson carried as a rule).
func (e *OpusMSEncoder) BeforeDestroy() {
	if e.enc != nil {
		C.opus_multistream_encoder_destroy(e.enc)
		e.enc = nil
	}
}

// OpusMSDecoder decodes multistream packets back to interleaved
// float32 — the consumer/test half.
type OpusMSDecoder struct {
	dec      *C.OpusMSDecoder
	channels int
	pcm      []float32
}

func NewOpusMSDecoder(channels int) (*OpusMSDecoder, error) {
	mapping := make([]C.uchar, channels)
	for i := range mapping {
		mapping[i] = C.uchar(i)
	}
	var cerr C.int
	dec := C.opus_multistream_decoder_create(
		C.opus_int32(common.SAMPLE_RATE),
		C.int(channels),
		C.int(channels),
		0,
		&mapping[0],
		&cerr,
	)
	if cerr != C.OPUS_OK || dec == nil {
		return nil, fmt.Errorf("inout: opus_multistream_decoder_create: %d", int(cerr))
	}
	return &OpusMSDecoder{
		dec:      dec,
		channels: channels,
		pcm:      make([]float32, common.FRAME_SIZE*channels),
	}, nil
}

// Decode decodes one packet to interleaved samples, valid until the
// next call.
func (d *OpusMSDecoder) Decode(pkt []byte) ([]float32, error) {
	n := C.opus_multistream_decode_float(
		d.dec,
		(*C.uchar)(unsafe.Pointer(&pkt[0])),
		C.opus_int32(len(pkt)),
		(*C.float)(unsafe.Pointer(&d.pcm[0])),
		C.int(len(d.pcm)/d.channels),
		0,
	)
	if n < 0 {
		return nil, fmt.Errorf("inout: opus_multistream_decode_float: %d", int(n))
	}
	return d.pcm[:int(n)*d.channels], nil
}

func (d *OpusMSDecoder) BeforeDestroy() {
	if d.dec != nil {
		C.opus_multistream_decoder_destroy(d.dec)
		d.dec = nil
	}
}
