package common

import (
	"math"
	"testing"
)

func TestChannelCount(t *testing.T) {

	if ChannelCountForOrder(2) != 9 {
		t.Fatalf(`bad channels 2 %v`, ChannelCountForOrder(2))
	}

	if ChannelCountForOrder(3) != 16 {
		t.Fatalf(`bad channels 3 %v`, ChannelCountForOrder(3))
	}

	if OrderForChannelCount(9) != 2 {
		t.Fatalf(`bad order 9 %v`, OrderForChannelCount(9))
	}

	if OrderForChannelCount(16) != 3 {
		t.Fatalf(`bad channels 16 %v`, OrderForChannelCount(16))
	}

}

func TestRect2Polar(t *testing.T) {

	pos := Position{X: 0.0, Y: -1.0, Z: 0.5}
	polar := TrigCartesianToPolar(pos)

	if polar.Azimuth != -90.0 {
		t.Fatalf(`bad azimuth %v`, polar)
	}
	if polar.Elevation != math.Atan(0.5)*RADIANS_TO_DEGREES {
		t.Fatalf(`bad elevation %v`, polar)
	}
}

func TestRect2Polar2(t *testing.T) {

	pos := Position{X: -1.0, Y: -1.0, Z: 0.5}
	polar := TrigCartesianToPolar(pos)

	if polar.Azimuth != -135.0 {
		t.Fatalf(`bad azimuth %v`, polar)
	}
	if polar.Elevation != math.Atan(0.5/math.Sqrt(2.0))*RADIANS_TO_DEGREES {
		t.Fatalf(`bad elevation %v`, polar)
	}
}

//func TestRect2PolarSpeed(t *testing.T) {
//
//	pos := Position{X: 1.0, Y: -1.0, Z: 0.5}
//	polar := TrigCartesianToPolar(pos)
//
//	before := time.Now()
//
//	for i := 0; i < 1024*1024; i++ {
//		polar = TrigCartesianToPolar(pos)
//	}
//	after := time.Now()
//
//	fmt.Printf("1M polars in 64 bit: %v\n", after.Sub(before))
//
//	if polar.Azimuth != -135.0 {
//		t.Fatalf(`bad azimuth %v`, polar)
//	}
//	if polar.Elevation != math.Atan(0.5/math.Sqrt(2.0))*RADIANS_TO_DEGREES {
//		t.Fatalf(`bad elevation %v`, polar)
//	}
//}
