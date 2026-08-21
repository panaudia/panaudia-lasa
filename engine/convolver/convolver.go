// Package convolver is the bilateral SH-domain overlap-save convolver
// (m3-design Q9/Q10): the in-repo cgo package that replaces the paired
// ambi_bin decode. Pure DSP — it knows taps and channel counts, not PAHRTF
// or spherical harmonics; core/binaural glues a hrtf.FilterBank to it.
//
// cgo compiles with no optimization by default; the convolver is hot-path
// (one process call per listener-frame), hence the explicit -O2.
package convolver

/*
#cgo CFLAGS: -O2
#cgo darwin LDFLAGS: -framework Accelerate
#cgo linux LDFLAGS: -lm
#include "pahconv.h"
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

func init() {
	if C.PAHCONV_BLOCK != common.FRAME_SIZE {
		panic("convolver: PAHCONV_BLOCK != common.FRAME_SIZE")
	}
}

// MaxTaps is the filter-length cap the FFT size supports (FFT-512 with a
// 240-sample block). hrtf.MaxFilterTaps mirrors it; the loader enforces it.
const MaxTaps = int(C.PAHCONV_MAX_TAPS)

// Set holds the frequency-domain filter bank, immutable and shared across
// listeners (safe for concurrent Process calls on different Handles).
type Set struct {
	c              *C.pahconv_set
	channelsPerEar int
}

// NewSet builds a set from ear-major taps [2][channelsPerEar][filterLen]
// (the hrtf.FilterBank layout). The taps are copied.
func NewSet(taps []float32, channelsPerEar, filterLen int) (*Set, error) {
	if len(taps) != 2*channelsPerEar*filterLen {
		return nil, fmt.Errorf("convolver: taps length %d != 2*%d*%d",
			len(taps), channelsPerEar, filterLen)
	}
	c := C.pahconv_set_create((*C.float)(unsafe.Pointer(&taps[0])),
		C.int(channelsPerEar), C.int(filterLen))
	if c == nil {
		return nil, fmt.Errorf("convolver: set create failed (channels %d, filter_len %d, cap %d)",
			channelsPerEar, filterLen, MaxTaps)
	}
	return &Set{c: c, channelsPerEar: channelsPerEar}, nil
}

func (s *Set) ChannelsPerEar() int {
	return s.channelsPerEar
}

func (s *Set) BeforeDestroy() {
	C.pahconv_set_destroy(s.c)
	s.c = nil
}

// Handle is the per-listener state: input history + scratch + set pointer.
// Created at connect, destroyed via the departure funnel, used by one
// goroutine at a time.
type Handle struct {
	c   *C.pahconv_handle
	set *Set
}

func NewHandle(set *Set) *Handle {
	c := C.pahconv_handle_create(set.c)
	if c == nil {
		panic("convolver: handle allocation failed")
	}
	return &Handle{c: c, set: set}
}

func (h *Handle) Reset() {
	C.pahconv_handle_reset(h.c)
}

func (h *Handle) BeforeDestroy() {
	C.pahconv_handle_destroy(h.c)
	h.c = nil
}

// Process runs one frame: src holds 2*ChannelsPerEar pointers to C-memory
// channel buffers of FRAME_SIZE samples (bus L channels first, then bus R);
// outL/outR are C-memory buffers of FRAME_SIZE samples. This is the single
// per-listener-frame cgo call; it allocates nothing.
func (h *Handle) Process(src []uintptr, outL, outR uintptr) {
	if len(src) != 2*h.set.channelsPerEar {
		panic(fmt.Sprintf("convolver: %d source channels, want %d",
			len(src), 2*h.set.channelsPerEar))
	}
	C.pahconv_process(h.c,
		(**C.float)(unsafe.Pointer(&src[0])),
		(*C.float)(unsafe.Pointer(outL)),
		(*C.float)(unsafe.Pointer(outR)))
}
