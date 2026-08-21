package common

import (
	"math"
)

const DEGREES_TO_RADIANS = math.Pi / 180.0
const RADIANS_TO_DEGREES = 180.0 / math.Pi

func TrigCartesianNormRectAndDistance(from Position, to Position) (Position, float64) {
	rect := Position{to.X - from.X, to.Y - from.Y, to.Z - from.Z}
	distance := math.Sqrt((rect.X * rect.X) + (rect.Y * rect.Y) + (rect.Z * rect.Z))
	if distance < 1e-10 {
		// Coincident positions: return forward direction and minimal distance
		return Position{1, 0, 0}, 1e-10
	}
	norm := Position{rect.X / distance, rect.Y / distance, rect.Z / distance}
	return norm, distance
}

func TrigCartesianRelativePosition(from Position, to Position) Position {
	return Position{to.X - from.X, to.Y - from.Y, to.Z - from.Z}
}

func TrigCartesianSumPosition(one Position, two Position) Position {
	return Position{one.X + two.X, one.Y + two.Y, one.Z + two.Z}
}

// TrigCartesianToPolar converts a position to polar in the engine frame:
// x forward, y left, z up — azimuth 0 at +X, positive anticlockwise
// (+90° at +Y), elevation positive upward, both in degrees.
func TrigCartesianToPolar(pos Position) PolarPosition {
	xxyy := (pos.X * pos.X) + (pos.Y * pos.Y)
	xxyyzz := xxyy + (pos.Z * pos.Z)
	a := math.Atan2(pos.Y, pos.X) * RADIANS_TO_DEGREES
	e := math.Atan2(pos.Z, math.Sqrt(xxyy)) * RADIANS_TO_DEGREES

	d := math.Sqrt(xxyyzz)
	if a < -180.0 {
		a = a + 360.0
	}
	if a > 180.0 {
		a = a - 360.0
	}
	if e < -90.0 {
		e = e + 180.0
	}
	if e > 90.0 {
		e = e - 180.0
	}
	return PolarPosition{a, e, d}
}
