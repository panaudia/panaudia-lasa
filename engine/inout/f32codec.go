package inout

import "unsafe"

// Carried verbatim from panaudia core/inout/encoding.go, whose NodeInfo
// state-plane codecs were deliberately left behind (see PROVENANCE.md);
// these zero-copy float32<->byte views are the only part the audio path uses.

func Encodef32(fs []float32) []byte {
	return encodeUnsafe(fs)
}

func Decodef32(bs []byte) []float32 {
	return decodeUnsafe(bs)
}

func encodeUnsafe(fs []float32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&fs[0])), len(fs)*4)
}

func decodeUnsafe(bs []byte) []float32 {
	return unsafe.Slice((*float32)(unsafe.Pointer(&bs[0])), len(bs)/4)
}
