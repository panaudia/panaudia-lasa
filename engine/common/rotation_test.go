package common

import (
	"math"
	"testing"
)

// Identity rotation must produce the exact identity matrix (cos(0)=1,
// sin(0)=0 are exact in IEEE 754), so applying it is bit-transparent —
// the M1 flag-on/identity ≡ flag-off anchor depends on this.
func TestMatrixFromRotationIdentityIsExact(t *testing.T) {
	m := MatrixFromRotation(Rotation{})
	want := RotationMatrix33{1, 0, 0, 0, 1, 0, 0, 0, 1}
	if m != want {
		t.Fatalf("identity rotation produced non-exact identity matrix: %v", m)
	}
	p := Position{X: 0.123456789, Y: -0.987654321, Z: 0.5}
	if got := m.Apply(p); got != p {
		t.Fatalf("identity apply not bit-transparent: %v != %v", got, p)
	}
}

// SAF's getRz is the transpose of the standard CCW rotation: for yaw=+90°,
// R·x̂ = -ŷ and R·ŷ = x̂ (frame rotation). This pins the sign convention
// against saf_utility_geometry.c getRz.
func TestMatrixFromRotationYawSignConvention(t *testing.T) {
	m := MatrixFromRotation(Rotation{Yaw: 90})
	gotX := m.Apply(Position{X: 1})
	if math.Abs(gotX.X) > 1e-12 || math.Abs(gotX.Y-(-1)) > 1e-12 || math.Abs(gotX.Z) > 1e-12 {
		t.Fatalf("yaw+90 · x̂ = %v, want (0,-1,0) per SAF getRz", gotX)
	}
	gotY := m.Apply(Position{Y: 1})
	if math.Abs(gotY.X-1) > 1e-12 || math.Abs(gotY.Y) > 1e-12 || math.Abs(gotY.Z) > 1e-12 {
		t.Fatalf("yaw+90 · ŷ = %v, want (1,0,0) per SAF getRz", gotY)
	}
}

// Any rotation matrix must be orthonormal: |R·v| = |v| and R·Rᵀ = I.
func TestMatrixFromRotationOrthonormal(t *testing.T) {
	m := MatrixFromRotation(Rotation{Yaw: 37.5, Pitch: -21.25, Roll: 113})
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			var dot float64
			for k := 0; k < 3; k++ {
				dot += m[i*3+k] * m[j*3+k]
			}
			want := 0.0
			if i == j {
				want = 1.0
			}
			if math.Abs(dot-want) > 1e-12 {
				t.Fatalf("R·Rᵀ[%d][%d] = %v, want %v", i, j, dot, want)
			}
		}
	}
}
