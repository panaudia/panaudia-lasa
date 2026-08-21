//package inout
//
//import (
//	"math"
//)
//
//type SineMonoInput struct {
//	bufferSize  int
//	bufferIndex int
//	buffer      []float32
//	phase       float64
//}
//
//func NewSineMonoInput(Hz float64, rate int, size int) *SineMonoInput {
//	input := &SineMonoInput{
//		bufferSize:  size,
//		buffer:      make([]float32, size),
//		bufferIndex: 0,
//	}
//
//	// Pre-populate the buffer with one complete sine wave
//	step := Hz / float64(rate)
//	phase := 0.0
//
//	for i := 0; i < size; i++ {
//		input.buffer[i] = float32(math.Sin(phase * 2.0 * math.Pi))
//		_, phase = math.Modf(phase + step)
//	}
//
//	input.phase = phase // Store the final phase for continuity
//
//	return input
//}
//
//func (input *SineMonoInput) BeforeDestroy() {
//	// Clean up if needed
//}
//
//func (input *SineMonoInput) GetTick() int64 {
//	return 0
//}
//
//func (input *SineMonoInput) ReadMono(dst []float32) {
//	//fmt.Printf("ReadMono: %d\n", input.bufferIndex)
//	for i := range dst {
//		dst[i] = input.buffer[input.bufferIndex]
//		input.bufferIndex = (input.bufferIndex + 1) % input.bufferSize
//	}
//}
//
//func (input *SineMonoInput) GetNewBuffer() []float32 {
//	// Return a copy of the buffer
//	result := make([]float32, len(input.buffer))
//	copy(result, input.buffer)
//	return result
//}

package inout

import (
	"math"
	// "fmt"
)

type SineMonoInput struct {
	phase  float64
	step   float64
	buffer []float32
}

func NewSineMonoInput(Hz float64, rate int, size int) *SineMonoInput {
	input := SineMonoInput{}
	input.step = Hz / float64(rate)
	input.buffer = make([]float32, size)
	return &input
}

func (input *SineMonoInput) BeforeDestroy() {

}

func (input *SineMonoInput) ReadMono(dst []float32) {

	//fmt.Printf("ReadMono tone")
	for i := range dst {
		dst[i] = float32(math.Sin(input.phase * 2.0 * math.Pi))
		_, input.phase = math.Modf(input.phase + input.step)
	}
}

//func (input *SineMonoInput) ReadMonoIntoSlice(dst []float32) {
//	for i := range dst {
//		dst[i] = float32(math.Sin(input.phase * 2.0 * math.Pi))
//		_, input.phase = math.Modf(input.phase + input.step)
//	}
//}

func (input *SineMonoInput) GetNewBuffer() []float32 {
	for i := range input.buffer {
		input.buffer[i] = float32(math.Sin(input.phase * 2.0 * math.Pi))
		_, input.phase = math.Modf(input.phase + input.step)
	}

	return input.buffer
}
