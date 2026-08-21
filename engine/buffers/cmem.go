// C-heap allocation for CBuffer (M9.2, plan/m9-saf-exit/plan.md): the one
// thing this package still needs C for. Buffers live on the C heap so their
// pointers can cross cgo calls (convolver, opus) without Go-pointer rules
// in the way; everything else that panaudia-utils used to do to them is
// plain Go over unsafe.Slice views (see cbuffer.go).
package buffers

/*
#include <stdlib.h>
#include <string.h>

static void *pah_buffer_alloc(size_t bytes)
{
    void *p = NULL;
    if (posix_memalign(&p, 64, bytes) != 0)
        return NULL;
    memset(p, 0, bytes);
    return p;
}
*/
import "C"
import "unsafe"

// cAlloc returns a 64-byte-aligned, zeroed C-heap block of count float32s.
func cAlloc(count int) uintptr {
	p := C.pah_buffer_alloc(C.size_t(count) * C.size_t(unsafe.Sizeof(float32(0))))
	if p == nil {
		panic("buffers: C allocation failed")
	}
	return uintptr(p)
}

func cFree(p uintptr) {
	C.free(unsafe.Pointer(p)) //nolint:govet // p originates from C, not Go memory
}
