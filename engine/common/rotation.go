package common

import "math"

// RotationMatrix33 is a row-major 3×3 rotation matrix, applied as v' = M·v.
type RotationMatrix33 [9]float64

// MatrixFromRotation builds the head-rotation matrix exactly as SAF's
// ambi_bin does for its soundfield rotation — yawPitchRoll2Rzyx(yaw, pitch,
// roll, rollPitchYawFLAG=0, R), i.e. R = Rx(roll)·Ry(pitch)·Rz(yaw) with
// SAF's frame-rotation element signs (each factor is the transpose of the
// standard counterclockwise matrix), angles in degrees (ambi_bin_setYaw
// applies DEG2RAD). panaudia's decoder config keeps the RPY flag and all
// axis flips at 0, so this is the complete convention.
//
// getSHrotMtxReal(R) satisfies M_rot·Y(v) = Y(R·v), so encoding a source at
// direction R·v with a static decoder is exactly equivalent to encoding at v
// and rotating the soundfield at decode — the basis of the M1 equivalence
// test.
func MatrixFromRotation(rot Rotation) RotationMatrix33 {
	yaw := rot.Yaw * math.Pi / 180.0
	pitch := rot.Pitch * math.Pi / 180.0
	roll := rot.Roll * math.Pi / 180.0

	cy, sy := math.Cos(yaw), math.Sin(yaw)
	cp, sp := math.Cos(pitch), math.Sin(pitch)
	cr, sr := math.Cos(roll), math.Sin(roll)

	// SAF getRz / getRy / getRx element layouts:
	rz := RotationMatrix33{
		cy, sy, 0,
		-sy, cy, 0,
		0, 0, 1,
	}
	ry := RotationMatrix33{
		cp, 0, -sp,
		0, 1, 0,
		sp, 0, cp,
	}
	rx := RotationMatrix33{
		1, 0, 0,
		0, cr, sr,
		0, -sr, cr,
	}

	return mul33(rx, mul33(ry, rz))
}

func mul33(a, b RotationMatrix33) RotationMatrix33 {
	var out RotationMatrix33
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			out[i*3+j] = a[i*3]*b[j] + a[i*3+1]*b[3+j] + a[i*3+2]*b[6+j]
		}
	}
	return out
}

// Apply rotates a vector (or direction): v' = M·v.
func (m *RotationMatrix33) Apply(p Position) Position {
	return Position{
		X: m[0]*p.X + m[1]*p.Y + m[2]*p.Z,
		Y: m[3]*p.X + m[4]*p.Y + m[5]*p.Z,
		Z: m[6]*p.X + m[7]*p.Y + m[8]*p.Z,
	}
}
