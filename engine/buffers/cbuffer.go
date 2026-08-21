package buffers

import (
	"unsafe"
)

// CBuffer is a float32 buffer on the C heap (see cmem.go for why). The ops
// below are plain Go over unsafe.Slice views — bit-identical to the
// panaudia-utils C they replace (M9.2, parity-pinned by
// cbuffer_migration_test.go until the spacer path retires at M9.5).
type CBuffer struct {
	p_data  uintptr
	count   int
	isChild bool
}

func (buffer *CBuffer) GetDataPointer() uintptr {
	return buffer.p_data
}

func (buffer *CBuffer) GetSize() int {
	return buffer.count
}

func NewCBuffer(size int) *CBuffer {
	return &CBuffer{
		p_data:  cAlloc(size), // zeroed
		count:   size,
		isChild: false,
	}
}

// the data is not allocated but uses the parent buffer's data
func NewChildCBuffer(size int, parent *CBuffer, position int) *CBuffer {
	if position+size > parent.count {
		panic("NewChildCBuffer going past end of parent data")
	}
	return &CBuffer{
		p_data:  parent.p_data + uintptr(position)*4,
		count:   size,
		isChild: true,
	}
}

func (buffer *CBuffer) BeforeDestroy() {
	if !buffer.isChild {
		cFree(buffer.p_data)
	}
	buffer.p_data = 0
}

func (buffer *CBuffer) Clear() {
	s := buffer.AsUnsafeFloatSlice()
	for i := range s {
		s[i] = 0
	}
}

func (buffer *CBuffer) CopyFromSlice(src []float32) {
	// Bound by len(src): a shorter-than-buffer source (e.g. a misconfigured
	// sink) must not read past its end.
	copy(buffer.AsUnsafeFloatSlice(), src)
}

func (buffer *CBuffer) CopyFromIntSlice(src []int32) {
	copy(buffer.AsUnsafeInt32Slice(), src)
}

func (buffer *CBuffer) CopyToSlice(dst []float32) {
	copy(dst, buffer.AsUnsafeFloatSlice())
}

func (buffer *CBuffer) CopyFromCBuffer(other *CBuffer) {
	copy(buffer.AsUnsafeFloatSlice(), other.AsUnsafeFloatSlice()[:other.count])
}

func (buffer *CBuffer) AddCBuffer(other *CBuffer) {
	buffer.AddCBufferLength(other, buffer.count)
}

func (buffer *CBuffer) AddCBufferLength(other *CBuffer, length int) {
	dst := buffer.AsUnsafeFloatSlice()[:length]
	src := other.AsUnsafeFloatSlice()[:length]
	for i := range dst {
		dst[i] += src[i]
	}
}

func (buffer *CBuffer) InterleaveCBuffers(otherA *CBuffer, otherB *CBuffer) {
	n := buffer.count / 2
	dst := buffer.AsUnsafeFloatSlice()
	a := otherA.AsUnsafeFloatSlice()[:n]
	b := otherB.AsUnsafeFloatSlice()[:n]
	for i := 0; i < n; i++ {
		dst[2*i] = a[i]
		dst[2*i+1] = b[i]
	}
}

func (buffer *CBuffer) SumInterleavedCBuffer(other *CBuffer) {
	dst := buffer.AsUnsafeFloatSlice()
	src := other.AsUnsafeFloatSlice()
	for i := range dst {
		dst[i] = (src[2*i] + src[2*i+1]) / 2
	}
}

func (buffer *CBuffer) Scale(scale float32) {
	s := buffer.AsUnsafeFloatSlice()
	for i := range s {
		s[i] *= scale
	}
}

func (buffer *CBuffer) AsUnsafeByteSlice() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(buffer.p_data)), buffer.count*4)
}

func (buffer *CBuffer) AsUnsafeFloatSlice() []float32 {
	return unsafe.Slice((*float32)(unsafe.Pointer(buffer.p_data)), buffer.count)
}

func (buffer *CBuffer) AsUnsafeInt32Slice() []int32 {
	return unsafe.Slice((*int32)(unsafe.Pointer(buffer.p_data)), buffer.count)
}
