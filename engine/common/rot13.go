
package common

import (
	"io"
)

func rot13(b byte) byte {
	var a, z byte
	switch {
	case 'a' <= b && b <= 'z':
		a, z = 'a', 'z'
	case 'A' <= b && b <= 'Z':
		a, z = 'A', 'Z'
	default:
		return b
	}
	// return (b-a+13)%26 + a
	return (b-a+13)%(z-a+1) + a
}

type RotReader struct {
	R io.Reader
}

func (r RotReader) Read(p []byte){
	//n, err := r.R.Read(p) // remove R to get stack overflow error :-)

	for i := 0; i < len(p); i++ {
		p[i] = rot13(p[i])
	}
}