package convolver

import (
	"math"
	"math/rand"
	"testing"

	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/common"
	"github.com/panaudia/panaudia-lasa/engine/hrtf"
)

// rig owns the C-memory buffers a Process call needs.
type rig struct {
	set     *Set
	handle  *Handle
	in      []*buffers.CBuffer // 2*nCh channel buffers
	inPtrs  []uintptr
	outL    *buffers.CBuffer
	outR    *buffers.CBuffer
	nCh     int
	destroy []func()
}

func newRig(t *testing.T, taps []float32, nCh, filterLen int) *rig {
	t.Helper()
	set, err := NewSet(taps, nCh, filterLen)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{set: set, handle: NewHandle(set), nCh: nCh}
	for i := 0; i < 2*nCh; i++ {
		b := buffers.NewCBuffer(common.FRAME_SIZE)
		r.in = append(r.in, b)
		r.inPtrs = append(r.inPtrs, b.GetDataPointer())
	}
	r.outL = buffers.NewCBuffer(common.FRAME_SIZE)
	r.outR = buffers.NewCBuffer(common.FRAME_SIZE)
	t.Cleanup(func() {
		r.handle.BeforeDestroy()
		r.set.BeforeDestroy()
		for _, b := range r.in {
			b.BeforeDestroy()
		}
		r.outL.BeforeDestroy()
		r.outR.BeforeDestroy()
	})
	return r
}

// processFrame feeds one frame (per-channel slices, zero-filled if nil) and
// returns copies of the two ear outputs.
func (r *rig) processFrame(frames [][]float32) (l, r_ []float32) {
	zero := make([]float32, common.FRAME_SIZE)
	for i, b := range r.in {
		src := zero
		if frames != nil && frames[i] != nil {
			src = frames[i]
		}
		b.CopyFromSlice(src)
	}
	r.handle.Process(r.inPtrs, r.outL.GetDataPointer(), r.outR.GetDataPointer())
	l = append([]float32(nil), r.outL.AsUnsafeFloatSlice()...)
	r_ = append([]float32(nil), r.outR.AsUnsafeFloatSlice()...)
	return
}

// refConvolve is the float64 ground truth: per-ear sum over channels of the
// full linear convolution, evaluated over nFrames frames.
func refConvolve(taps []float32, nCh, filterLen int, streams [][]float32, ear int, nSamples int) []float64 {
	out := make([]float64, nSamples)
	for ch := 0; ch < nCh; ch++ {
		h := taps[(ear*nCh+ch)*filterLen : (ear*nCh+ch+1)*filterLen]
		x := streams[ear*nCh+ch]
		for n := 0; n < nSamples; n++ {
			var acc float64
			for k := 0; k < filterLen && k <= n; k++ {
				acc += float64(h[k]) * float64(x[n-k])
			}
			out[n] += acc
		}
	}
	return out
}

func TestImpulseExact(t *testing.T) {
	// Single channel per ear, delta filter at tap 5: an input impulse must
	// come out as a delta at sample 5, and the silent bus stays exactly zero.
	filterLen := 64
	taps := make([]float32, 2*1*filterLen)
	taps[5] = 1.0            // ear L, ch 0
	taps[filterLen+9] = 1.0  // ear R, ch 0
	r := newRig(t, taps, 1, filterLen)

	impulse := make([]float32, common.FRAME_SIZE)
	impulse[0] = 1.0
	l, rr := r.processFrame([][]float32{impulse, nil})

	for i := range l {
		want := 0.0
		if i == 5 {
			want = 1.0
		}
		if math.Abs(float64(l[i])-want) > 2e-6 {
			t.Fatalf("outL[%d] = %g, want %g", i, l[i], want)
		}
	}
	for i := range rr {
		if rr[i] != 0 {
			t.Fatalf("outR[%d] = %g, want exact 0 (silent bus)", i, rr[i])
		}
	}
}

func TestStreamingMatchesReference(t *testing.T) {
	// Random taps and input over several frames: the overlap-save chain must
	// match a float64 direct convolution across every block boundary.
	nCh, filterLen, nFrames := 4, 272, 6
	rng := rand.New(rand.NewSource(1))
	taps := make([]float32, 2*nCh*filterLen)
	for i := range taps {
		taps[i] = rng.Float32()*2 - 1
	}
	nSamples := nFrames * common.FRAME_SIZE
	streams := make([][]float32, 2*nCh)
	for i := range streams {
		streams[i] = make([]float32, nSamples)
		for n := range streams[i] {
			streams[i][n] = rng.Float32()*2 - 1
		}
	}

	r := newRig(t, taps, nCh, filterLen)
	gotL := make([]float32, 0, nSamples)
	gotR := make([]float32, 0, nSamples)
	for f := 0; f < nFrames; f++ {
		frames := make([][]float32, 2*nCh)
		for i := range frames {
			frames[i] = streams[i][f*common.FRAME_SIZE : (f+1)*common.FRAME_SIZE]
		}
		l, rr := r.processFrame(frames)
		gotL = append(gotL, l...)
		gotR = append(gotR, rr...)
	}

	for ear, got := range [][]float32{gotL, gotR} {
		want := refConvolve(taps, nCh, filterLen, streams, ear, nSamples)
		var peak float64
		for _, w := range want {
			peak = math.Max(peak, math.Abs(w))
		}
		for n := range want {
			if math.Abs(float64(got[n])-want[n]) > 1e-5*peak {
				t.Fatalf("ear %d sample %d: got %g want %g (peak %g)",
					ear, n, got[n], want[n], peak)
			}
		}
	}
}

func TestResetRepeats(t *testing.T) {
	nCh, filterLen := 2, 128
	rng := rand.New(rand.NewSource(2))
	taps := make([]float32, 2*nCh*filterLen)
	for i := range taps {
		taps[i] = rng.Float32()*2 - 1
	}
	frame := make([][]float32, 2*nCh)
	for i := range frame {
		frame[i] = make([]float32, common.FRAME_SIZE)
		for n := range frame[i] {
			frame[i][n] = rng.Float32()*2 - 1
		}
	}
	r := newRig(t, taps, nCh, filterLen)
	l1, r1 := r.processFrame(frame)
	r.processFrame(frame) // dirty the history
	r.handle.Reset()
	l2, r2 := r.processFrame(frame)
	for i := range l1 {
		if l1[i] != l2[i] || r1[i] != r2[i] {
			t.Fatalf("sample %d differs after Reset: %g/%g vs %g/%g",
				i, l1[i], r1[i], l2[i], r2[i])
		}
	}
}

func TestRealBankImpulseIsTaps(t *testing.T) {
	// Feed a unit impulse into the W channel of bus L: the left output must
	// reproduce the left-ear W filter taps themselves, across the frame
	// boundary (taps 240..271 arrive in frame 2).
	bank := hrtf.Default().Bank(3)
	set, err := NewSet(bank.Taps, bank.NumChannels, bank.FilterLen)
	if err != nil {
		t.Fatal(err)
	}
	defer set.BeforeDestroy()
	r := &rig{set: set, handle: NewHandle(set), nCh: bank.NumChannels}
	for i := 0; i < 2*bank.NumChannels; i++ {
		b := buffers.NewCBuffer(common.FRAME_SIZE)
		r.in = append(r.in, b)
		r.inPtrs = append(r.inPtrs, b.GetDataPointer())
	}
	r.outL = buffers.NewCBuffer(common.FRAME_SIZE)
	r.outR = buffers.NewCBuffer(common.FRAME_SIZE)
	defer func() {
		r.handle.BeforeDestroy()
		for _, b := range r.in {
			b.BeforeDestroy()
		}
		r.outL.BeforeDestroy()
		r.outR.BeforeDestroy()
	}()

	impulse := make([]float32, common.FRAME_SIZE)
	impulse[0] = 1.0
	frames := make([][]float32, 2*bank.NumChannels)
	frames[0] = impulse
	l1, _ := r.processFrame(frames)
	l2, _ := r.processFrame(nil)

	w := bank.EarTaps(0, 0)
	got := append(l1, l2...)
	for i := 0; i < 2*common.FRAME_SIZE; i++ {
		want := 0.0
		if i < len(w) {
			want = float64(w[i])
		}
		if math.Abs(float64(got[i])-want) > 1e-6 {
			t.Fatalf("sample %d: got %g, want tap %g", i, got[i], want)
		}
	}
}

func TestSetRejectsBadShapes(t *testing.T) {
	if _, err := NewSet(make([]float32, 2*1*(MaxTaps+1)), 1, MaxTaps+1); err == nil {
		t.Error("accepted filter_len over cap")
	}
	if _, err := NewSet(make([]float32, 10), 1, 64); err == nil {
		t.Error("accepted mismatched taps length")
	}
}

func BenchmarkProcessOrder3(b *testing.B) {
	bank := hrtf.Default().Bank(3)
	set, err := NewSet(bank.Taps, bank.NumChannels, bank.FilterLen)
	if err != nil {
		b.Fatal(err)
	}
	defer set.BeforeDestroy()
	handle := NewHandle(set)
	defer handle.BeforeDestroy()
	var ins []*buffers.CBuffer
	var ptrs []uintptr
	for i := 0; i < 2*bank.NumChannels; i++ {
		buf := buffers.NewCBuffer(common.FRAME_SIZE)
		noise := make([]float32, common.FRAME_SIZE)
		for n := range noise {
			noise[n] = rand.Float32()*2 - 1
		}
		buf.CopyFromSlice(noise)
		ins = append(ins, buf)
		ptrs = append(ptrs, buf.GetDataPointer())
	}
	outL := buffers.NewCBuffer(common.FRAME_SIZE)
	outR := buffers.NewCBuffer(common.FRAME_SIZE)
	defer func() {
		for _, buf := range ins {
			buf.BeforeDestroy()
		}
		outL.BeforeDestroy()
		outR.BeforeDestroy()
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		handle.Process(ptrs, outL.GetDataPointer(), outR.GetDataPointer())
	}
}
