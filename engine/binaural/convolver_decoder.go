package binaural

import (
	"fmt"
	"math"

	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/common"
	"github.com/panaudia/panaudia-lasa/engine/convolver"
	"github.com/panaudia/panaudia-lasa/engine/hrtf"
)

// NewConvolverSet builds the shared bilateral filter set for the runtime
// channel count from a loaded PAHRTF set. Called once at startup; the set is
// immutable, shared by every listener's ConvolverDecoder, and lives for the
// process — it is never destroyed per listener.
// From M4 the flag-on encode always reinserts Woodworth model delays, so
// the decode set MUST be ear-aligned with the matching method — a
// magls-full set here would render double ITD. This is the use-site half
// of the alignment-consistency invariant (the loader validates internal
// consistency; this validates set-against-runtime).
func NewConvolverSet(set *hrtf.Set, channelCount int) *convolver.Set {
	if set.Manifest.FilterType != hrtf.FilterTypeEarAligned ||
		set.Manifest.Alignment.Method != "woodworth" {
		panic(fmt.Sprintf(
			"binaural: flag-on decode needs a %s set with woodworth alignment (encode reinserts model delays); got %q/%q",
			hrtf.FilterTypeEarAligned, set.Manifest.FilterType, set.Manifest.Alignment.Method))
	}
	order := int(math.Sqrt(float64(channelCount))) - 1
	bank := set.Bank(order)
	if bank == nil {
		panic(fmt.Sprintf("binaural: HRTF set %q carries no order-%d filter bank (orders %v)",
			set.Manifest.Name, order, set.Manifest.Orders))
	}
	cset, err := convolver.NewSet(bank.Taps, bank.NumChannels, bank.FilterLen)
	if err != nil {
		panic("binaural: " + err.Error())
	}
	return cset
}

// ConvolverDecoder is the flag-on bilateral decode (M3, m3-design Q11): one
// custom per-ear SH convolver replacing the paired-ambi_bin M2 scaffolding.
// It consumes both ear buses in a single call and interleaves the two ear
// signals into StereoBuffer, mirroring BinauralDecoder's output contract.
// One per listener output — it owns the per-listener convolver handle —
// created at connect, destroyed via the departure funnel.
type ConvolverDecoder struct {
	handle       *convolver.Handle
	leftBuffer   *buffers.CBuffer
	rightBuffer  *buffers.CBuffer
	StereoBuffer *buffers.CBuffer
}

func NewConvolverDecoder(set *convolver.Set) *ConvolverDecoder {
	dec := ConvolverDecoder{}
	dec.handle = convolver.NewHandle(set)
	dec.leftBuffer = buffers.NewCBuffer(common.FRAME_SIZE)
	dec.rightBuffer = buffers.NewCBuffer(common.FRAME_SIZE)
	dec.StereoBuffer = buffers.NewCBuffer(common.FRAME_SIZE * 2)
	return &dec
}

// BilateralToStereo decodes one frame: src holds both buses' channel
// pointers (bus L's channels first, then bus R's, matching Encoder.Output).
func (dec *ConvolverDecoder) BilateralToStereo(src []uintptr) {
	dec.handle.Process(src, dec.leftBuffer.GetDataPointer(), dec.rightBuffer.GetDataPointer())
	dec.StereoBuffer.InterleaveCBuffers(dec.leftBuffer, dec.rightBuffer)
}

// LeftCBuffer / RightCBuffer expose the per-ear signals (test access, and
// symmetry with BinauralDecoder).
func (dec *ConvolverDecoder) LeftCBuffer() *buffers.CBuffer {
	return dec.leftBuffer
}

func (dec *ConvolverDecoder) RightCBuffer() *buffers.CBuffer {
	return dec.rightBuffer
}

func (dec *ConvolverDecoder) Reset() {
	dec.handle.Reset()
	dec.leftBuffer.Clear()
	dec.rightBuffer.Clear()
	dec.StereoBuffer.Clear()
}

func (dec *ConvolverDecoder) BeforeDestroy() {
	dec.handle.BeforeDestroy()
	dec.leftBuffer.BeforeDestroy()
	dec.rightBuffer.BeforeDestroy()
	dec.StereoBuffer.BeforeDestroy()
}
