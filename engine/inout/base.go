package inout

type MonoInput interface {
	ReadMono(dst []float32)
	BeforeDestroy()
}

type AmbisonicOutput interface {
	WriteAmbisonic(ambisonicChannels []float32)
	BeforeDestroy()
}
