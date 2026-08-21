package ambisonic

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

type Desired struct {
	Position common.Position
	Weights  []float32
}

var desiredWeights = []Desired{
	{
		Position: common.Position{X: 0.5, Y: 0.52, Z: 0.5},
		Weights:  []float32{1, 1.7320508, 0, 0, 0, 0, -1.118034, 0, -1.9364916},
	},
	{
		Position: common.Position{X: 0.5, Y: 0.55, Z: 0.55},
		Weights:  []float32{1, 1.2247448, 1.2247448, 0, 0, 1.9364915, 0.5590169, 0, -0.9682458},
	},
	{
		Position: common.Position{X: 0.5, Y: 0.4, Z: 0.5},
		Weights:  []float32{1, -1.7320508, 0, 0, 0, 0, -1.118034, -0, -1.9364916},
	},
	{
		Position: common.Position{X: 0.5, Y: 0.6, Z: 0.5},
		Weights:  []float32{1, 1.7320508, 0, 0, 0, 0, -1.118034, 0, -1.9364916},
	},
	{
		Position: common.Position{X: 0.6, Y: 0.5, Z: 0.5},
		Weights:  []float32{1, -0, 0, 1.7320508, -0, -0, -1.118034, 0, 1.9364916},
	},
	{
		Position: common.Position{X: 0.4, Y: 0.5, Z: 0.5},
		Weights:  []float32{1, -1.5142069e-07, 0, -1.7320508, 3.3858694e-07, 0, -1.118034, -0, 1.9364916},
	},
	{
		Position: common.Position{X: 0.6, Y: 0.4, Z: 0.5},
		Weights:  []float32{1, -1.2247448, 0, 1.2247448, -1.9364916, 0, -1.118034, 0, -8.4646736e-08},
	},
	{
		Position: common.Position{X: 0.4, Y: 0.4, Z: 0.5},
		Weights:  []float32{1, -1.2247448, 0, -1.2247448, 1.9364916, -0, -1.118034, -0, 2.3092431e-08},
	},
	{
		Position: common.Position{X: 0.4, Y: 0.6, Z: 0.5},
		Weights:  []float32{1, 1.2247448, 0, -1.2247448, -1.9364916, 0, -1.118034, -0, 2.3092431e-08},
	},
	{
		Position: common.Position{X: 0.6, Y: 0.6, Z: 0.5},
		Weights:  []float32{1, 1.2247448, 0, 1.2247448, 1.9364916, 0, -1.118034, 0, -8.4646736e-08},
	},
}

var desiredWeights3 = []Desired{
	//{
	//	Position: common.Position{X: 0.5, Y: 0.52, Z: 0.5},
	//	Weights:  []float32{1, 1.7320508, 0, 0, -0, 0, -1.118034, -0, -1.9364916, -2.09165, -0, -1.6201851, -0, 0, -0, 0},
	//},
	{
		Position: common.Position{X: 0.5, Y: 0.55, Z: 0.55},
		Weights:  []float32{1, 1.2247448, 1.2247448, 0, 0, 1.9364915, 0.5590169, 0, -0.9682458, -0.73951, -1.5835954e-07, 1.7184657, -0.4677073, -7.511652e-08, -1.811422, 8.818568e-09},
	},
	//{
	//	Position: common.Position{X: 0.5, Y: 0.4, Z: 0.5},
	//	Weights:  []float32{1, -1.7320508, 0, 0, 0, 0, -1.118034, -0, -1.9364916},
	//},
	//{
	//	Position: common.Position{X: 0.5, Y: 0.6, Z: 0.5},
	//	Weights:  []float32{1, 1.7320508, 0, 0, 0, 0, -1.118034, 0, -1.9364916},
	//},
	//{
	//	Position: common.Position{X: 0.6, Y: 0.5, Z: 0.5},
	//	Weights:  []float32{1, -0, 0, 1.7320508, -0, -0, -1.118034, 0, 1.9364916},
	//},
	//{
	//	Position: common.Position{X: 0.4, Y: 0.5, Z: 0.5},
	//	Weights:  []float32{1, -1.5142069e-07, 0, -1.7320508, 3.3858694e-07, 0, -1.118034, -0, 1.9364916},
	//},
	//{
	//	Position: common.Position{X: 0.6, Y: 0.4, Z: 0.5},
	//	Weights:  []float32{1, -1.2247448, 0, 1.2247448, -1.9364916, 0, -1.118034, 0, -8.4646736e-08},
	//},
	//{
	//	Position: common.Position{X: 0.4, Y: 0.4, Z: 0.5},
	//	Weights:  []float32{1, -1.2247448, 0, -1.2247448, 1.9364916, -0, -1.118034, -0, 2.3092431e-08},
	//},
	//{
	//	Position: common.Position{X: 0.4, Y: 0.6, Z: 0.5},
	//	Weights:  []float32{1, 1.2247448, 0, -1.2247448, -1.9364916, 0, -1.118034, -0, 2.3092431e-08},
	//},
	//{
	//	Position: common.Position{X: 0.6, Y: 0.6, Z: 0.5},
	//	Weights:  []float32{1, 1.2247448, 0, 1.2247448, 1.9364916, 0, -1.118034, 0, -8.4646736e-08},
	//},
}

//func TestSAFMachineGivesDesiredWeights(t *testing.T) {
//	AssertSAFMachineGivesDesiredWeightsAny(desiredWeights, 2, t)
//}
//
//func TestSAFMachineGivesDesiredWeights3(t *testing.T) {
//	AssertSAFMachineGivesDesiredWeightsAny(desiredWeights3, 3, t)
//}

//func AssertSAFMachineGivesDesiredWeightsAny(dw []Desired, order int, t *testing.T) {
//
//	nMaxInputs := 3
//	channels := (order + 1) * (order + 1)
//
//	weightsMachine := saf.NewWeightsMachine(2.0, order)
//	weights := buffers.NewCBuffer(nMaxInputs * channels)
//	pWeights := weights.GetDataPointer()
//	fWeights := weights.AsUnsafeFloatSlice()
//
//	for _, desired := range dw {
//		weightsMachine.GetWeights(fWeights,
//			pWeights,
//			1.0,
//			2.0,
//			common.Position{X: 0.5, Y: 0.5, Z: 0.5},
//			desired.Position,
//			1,
//			nMaxInputs)
//
//		result := make([]float32, channels)
//
//		for i := 0; i < channels; i++ {
//			result[i] = fWeights[(i*3)+1]
//		}
//		common.AssertApproxArraysEqual(t, result, desired.Weights)
//	}
//}

// scaleToMeters converts a normalized 0–1 test position to meters for the
// meters-native GetWeights (design decision 9); the frozen SAF goldens
// (testdata/saf_sh_weights.json) were generated from the normalized+size
// form the retired GetWeightsSaf used.
func scaleToMeters(p common.Position, size float64) common.Position {
	return common.Position{X: p.X * size, Y: p.Y * size, Z: p.Z * size}
}

func TestEfficientMachineGivesDesiredWeights(t *testing.T) {

	weights := make([]float32, 9)

	for _, desired := range desiredWeights {
		GetWeights(weights,
			SQRT_4_PI,
			2.0,
			scaleToMeters(common.Position{X: 0.5, Y: 0.5, Z: 0.5}, 2.0),
			scaleToMeters(desired.Position, 2.0),
			nil,
			2,
			common.Position{X: 1}, 0, 0)

		// Every fixture sits 4–20 cm from the listener — inside the capped
		// near-gain region. The hand-computed expectations date from the
		// cap-at-1.0 era; the SH shape is unchanged, the level is now the
		// cap (NearFieldGainCap).
		want := make([]float32, len(desired.Weights))
		for i, w := range desired.Weights {
			want[i] = w * NearFieldGainCap
		}
		common.AssertApproxArraysEqual(t, weights, want)
	}
}

// TestBilateralGainLawParity pins the classic and bilateral render paths
// to the same distance-attenuation and reverb-split laws (review finding
// 10: the two paths once drifted when the near-gain cap changed on one
// side only). The W channel is direction-independent, so it must be
// bit-identical at every distance; beyond the parallax far-field bound the
// directions coincide too, so ALL channels must match.
func TestBilateralGainLawParity(t *testing.T) {
	setTestDelayModel(t)

	cfg := m1Config()
	listener := NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
	listener.SetPosition(common.Position{X: 0.5, Y: 0.5, Z: 0.5})
	peer := NewEncoder(uuid.New(), true, 1.0, 2.0, cfg, 1)

	ch := cfg.ChannelCount
	weightsL := make([]float32, ch)
	weightsR := make([]float32, ch)
	wet := make([]float32, common.REVERB_CHANNELS)
	dryClassic := make([]float32, ch)
	wetClassic := make([]float32, common.REVERB_CHANNELS)
	scratch := make([]float32, ch)

	for _, distM := range []float64{0.3, 0.58, 1.0, 1.5, 2.5, 6.0} {
		peer.SetPosition(common.Position{X: 0.5 + distM/cfg.Size, Y: 0.5, Z: 0.5})

		listener.bilateralWeights(peer, nil, weightsL, weightsR, wet)
		GetWeightsForReverb(dryClassic, wetClassic, scratch,
			peer.gainFactor, peer.peerAttenuationExponent,
			listener.PositionMeters, peer.PositionMeters, nil, cfg.Order, ch,
			common.Position{X: 1}, 0, 0)

		if weightsL[0] != dryClassic[0] || weightsR[0] != dryClassic[0] {
			t.Fatalf("d=%.2f m: dry W drifted — bilateral %v/%v vs classic %v",
				distM, weightsL[0], weightsR[0], dryClassic[0])
		}
		if wet[0] != wetClassic[0] {
			t.Fatalf("d=%.2f m: wet W drifted — bilateral %v vs classic %v",
				distM, wet[0], wetClassic[0])
		}
		if distM >= ParallaxFarFieldM {
			for i := 0; i < ch; i++ {
				if weightsL[i] != dryClassic[i] {
					t.Fatalf("d=%.2f m ch %d: far-field dry drifted — %v vs %v",
						distM, i, weightsL[i], dryClassic[i])
				}
			}
			for i := range wet {
				if wet[i] != wetClassic[i] {
					t.Fatalf("d=%.2f m ch %d: far-field wet drifted — %v vs %v",
						distM, i, wet[i], wetClassic[i])
				}
			}
		}
	}
}

func TestRandomMatchSecondOrder(t *testing.T) {
	AssertRandomMatch(2, t)
}

func TestRandomMatchThirdOrder(t *testing.T) {
	AssertRandomMatch(3, t)
}

func TestRandomMatchFourthOrder(t *testing.T) {
	AssertRandomMatch(4, t)
}

func TestRandomMatchFifthOrder(t *testing.T) {
	AssertRandomMatch(5, t)
}

// AssertRandomMatch checks the analytic SH evaluator against SAF getRSH
// outputs frozen at M9.4 (testdata/saf_sh_weights.json, generated from
// GetWeightsSaf before the SAF link was removed — the independent anchor
// the plan requires). Positions in the fixture were drawn with the same
// seeded RNG this test previously used live; the SAF and analytic paths
// diverge most near the poles, hence the fixed seed.
func AssertRandomMatch(order int, t *testing.T) {

	data, err := os.ReadFile("testdata/saf_sh_weights.json")
	if err != nil {
		t.Fatal(err)
	}
	var goldens struct {
		Cases map[string][]struct {
			Position [3]float64 `json:"pos"`
			Weights  []float32  `json:"w"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatal(err)
	}
	cases := goldens.Cases[strconv.Itoa(order)]
	if len(cases) == 0 {
		t.Fatalf("no golden cases for order %d", order)
	}

	channels := (order + 1) * (order + 1)
	efficientfWeights := make([]float32, channels)

	for _, c := range cases {
		pos := common.Position{X: c.Position[0], Y: c.Position[1], Z: c.Position[2]}
		GetWeights(efficientfWeights,
			SQRT_4_PI,
			2.0,
			scaleToMeters(common.Position{X: 0.5, Y: 0.5, Z: 0.5}, 2.0),
			scaleToMeters(pos, 2.0),
			nil,
			order,
			common.Position{X: 1}, 0, 0)

		common.AssertApproxArraysEqual(t, c.Weights, efficientfWeights)
	}
}
