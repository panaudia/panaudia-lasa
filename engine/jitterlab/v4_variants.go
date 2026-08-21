package jitterlab

// v4_variants.go — the controller variants behind the design §9 seam,
// kept runnable per the leave-alternatives-testable requirement. The
// shipping law is PServo (v4.go); these exist so the golden suite can
// price the alternatives instead of the design arguing about them
// (TestVariantComparisonV4 produces the matrix; verdicts recorded in
// the design doc §9/§14). All share the shipped macro-trim (confirmed
// by window min, sized by median).

// softClamp passes v through unchanged inside ±c and rolls it off as
// c²/|v| beyond — continuous at ±c, → 0 as |v| → ∞. The
// beyond-plausible-drift rejection used by the shipped FF.
func softClamp(v, c float64) float64 {
	if v > c || v < -c {
		return c * c / v
	}
	return v
}

func clampRate(v, cap_ float64) float64 {
	if v > cap_ {
		return cap_
	}
	if v < -cap_ {
		return -cap_
	}
	return v
}

// PAnchorOnly is the draft §5's original law: a two-sided proportional
// servo on the median, no feed-forward — the design-review "P-only"
// resolution, refuted in bring-up for the pre-F1 geometry (§14 F2).
// With the F1 setpoint floor a drift-down quantum miss now PERSISTS in
// the median instead of clicking through the underrun valve, so the
// comparison re-tests it honestly. Gain defaults to the draft's
// arithmetic (≈ 10 splices/s per ms — it must absorb drift alone).
type PAnchorOnly struct {
	Gain float64 // 0 → 0.2 (frames/s per frame)
}

func (c PAnchorOnly) Decide(est WindowEstimate, geo ServoGeometry) (float64, int64) {
	if est.MinRaw-geo.Setpoint > geo.Theta {
		return 0, est.MedianRaw - geo.Setpoint
	}
	g := c.Gain
	if g == 0 {
		g = 0.2
	}
	switch e := est.MedianRaw - geo.Setpoint; {
	case e > geo.Deadband:
		return clampRate(g*float64(e-geo.Deadband), geo.RateCapPerSec), 0
	case e < -geo.Deadband:
		return clampRate(-g*float64(-e-geo.Deadband), geo.RateCapPerSec), 0
	}
	return 0, 0
}

// PIServo is the roc/zita-family law: proportional + integrator, the
// integrator learning the drift rate (zero standing error). Anti-windup
// is the integrator clamp at the plausible-drift bound. The survey's
// predicted pathology (roc §3): a sustained unwinnable deficit —
// packet loss — winds the integrator to its clamp and grinds inserts
// indefinitely; the comparison measures exactly that. Stateful: one
// instance per buffer.
type PIServo struct {
	KP float64 // 0 → 0.01 (the shipped anchor gain)
	KI float64 // per-window integrator gain; 0 → 0.005
	i  float64 // learned rate, frames/s
}

func (c *PIServo) Decide(est WindowEstimate, geo ServoGeometry) (float64, int64) {
	if est.MinRaw-geo.Setpoint > geo.Theta {
		return 0, est.MedianRaw - geo.Setpoint
	}
	kp, ki := c.KP, c.KI
	if kp == 0 {
		kp = 0.01
	}
	if ki == 0 {
		ki = 0.005
	}
	e := float64(est.MedianRaw - geo.Setpoint)
	c.i += ki * e
	if c.i > geo.FFClampPerSec {
		c.i = geo.FFClampPerSec
	} else if c.i < -geo.FFClampPerSec {
		c.i = -geo.FFClampPerSec
	}
	var p float64
	if e > float64(geo.Deadband) {
		p = kp * (e - float64(geo.Deadband))
	} else if e < -float64(geo.Deadband) {
		p = kp * (e + float64(geo.Deadband))
	}
	return clampRate(p+c.i, geo.RateCapPerSec), 0
}

// RateOnly is the design's live simplification: the servo is a pure
// rate loop (the shipped FF alone) and ALL position work goes to the
// macro-trim. No anchor: fill wanders on FF estimation residue (AOO's
// documented weakness) and on inherited offsets below Θ, which nothing
// corrects. The comparison prices that wander.
type RateOnly struct{}

func (RateOnly) Decide(est WindowEstimate, geo ServoGeometry) (float64, int64) {
	if est.MinRaw-geo.Setpoint > geo.Theta {
		return 0, est.MedianRaw - geo.Setpoint
	}
	return clampRate(softClamp(est.SlopeFPS, geo.FFClampPerSec), geo.RateCapPerSec), 0
}
