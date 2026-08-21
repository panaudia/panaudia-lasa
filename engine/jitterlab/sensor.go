package jitterlab

// sensor.go — the sensor lab's candidate sensors (plan/jitter-v4-harness.md,
// "sensor lab"). Sensors are pure observers: fed per-write and per-read
// observations, they emit per-window estimates of the quantities a v4
// controller would need — reference level, per-side width, drift. They
// are deliberately implementable inside a production buffer (integer
// state, monotonic deques, no wall-clock — the read count is the clock),
// except TimestampSensor, which uses master-clock arrival times and
// exists as the harness-only upper bound: it measures what wire
// timestamping would buy.
//
// The two fill-domain candidates share this code and differ only in feed:
//
//	virtual fill = delivered − R·tick   (write-side only; actuation-free)
//	raw fill     = wp − rp              (attached mode; includes the
//	                                     buffer's corrections, snaps and
//	                                     underrun freezes — the pollution
//	                                     the lab measures)
//
// Sign conventions (design-space doc): transit delay only pushes fill
// DOWN, so the windowed MAX is the clean reference and its slope is
// drift. Per-side widths are split at the window MEDIAN (a robust mid —
// clump spikes barely move it): late arrivals below it, reader-stall
// accumulation above it (visible in raw fill).

import "slices"

// Obs is one per-read observation fed to sensors.
type Obs struct {
	Tick      int64 // read index — the sensor's clock
	Time      int64 // master clock (harness-only; fill sensors must not use)
	Delivered int64 // cumulative frames delivered to the buffer so far
	Fill      int64 // PRE-read fill in frames (before the buffer acts); -1 in passive mode
}

// Estimate is one closed sensing window's output.
type Estimate struct {
	Tick      int64   // clock at close: read index (fill sensors) / stream pos (TimestampSensor)
	Ref       int64   // reference level (fill: windowed max; timestamps: windowed min transit)
	Mean      float64 // window mean of the signal (fill sensors; 0 for timestamps)
	Median    int64   // window median — the robust mid-reference (fill sensors)
	WidthLow  int64   // late-side spread: median − window min (fill) / max−min transit (timestamps)
	WidthHigh int64   // high-side spread: window max − median (fill; reader stalls live here)
	// DriftPPM is the mean-slope drift estimate over the horizon. Lab
	// finding (2026-08-11): for fill sensors it is PACKET-QUANTIZED —
	// arrival events carry no sub-packet timing, so its resolution is
	// one packet per horizon (≈278 ppm at N=400, K=10, W=R=240). Fine
	// drift needs the Ref slope over long horizons (lattice-quantized
	// but unbiased) or timestamps (TimestampSensor resolves ppm-level
	// drift in seconds — that is precisely what timestamps buy).
	DriftPPM float64
	Frozen   bool // window closed while delivery was frozen
}

// Sensor is the lab's observer interface.
type Sensor interface {
	OnWrite(time, pos, frames int64)
	OnRead(o Obs)
	Estimates() []Estimate
}

// EnvelopeSensorConfig tunes an EnvelopeSensor. Zero fields default.
type EnvelopeSensorConfig struct {
	WindowReads  int64 // N: window length in pushed reads. Default 400.
	Horizon      int64 // K: windows kept for the drift estimate. Default 10.
	FreezeReads  int64 // no-delivery run that freezes sensing. Default max(8, 40ms/R).
	ReaderFrames int64 // R, for the virtual signal and ppm conversion. Required.
	UseRawFill   bool  // sense Obs.Fill instead of the virtual signal
}

// EnvelopeSensor is the fill-domain candidate: monotonic min/max deques
// over a tumbling window, window means for a fine-resolution drift slope,
// and a freeze-and-reanchor rule so outages teach it nothing: after
// FreezeReads with no delivery it stops sensing, and on resumption it
// re-anchors the signal to the last delivered level — a loss-induced
// step (outage, device stall) is excised rather than learned. The
// mean-slope drift estimate is fine-grained but burst-polluted; the
// burst-immune alternative is the slope of Ref across estimates, which
// the lattice quantizes to whole packets (computed by consumers over
// longer horizons — see the lab tests).
type EnvelopeSensor struct {
	cfg EnvelopeSensorConfig

	prevDelivered int64
	gapRun        int64
	frozen        bool
	lastLevel     int64 // signal at the last delivered read (pre-anchor)
	anchor        int64

	pushes  int64
	curTick int64
	maxDQ   []tickVal
	minDQ   []tickVal
	winSum  float64
	winN    int64
	winBuf  []int64 // window samples, for the median
	sortBuf []int64

	means []float64 // ring of recent window means, cap Horizon

	ests []Estimate
}

type tickVal struct {
	idx int64
	v   int64
}

// NewEnvelopeSensor builds an EnvelopeSensor. ReaderFrames must be set.
func NewEnvelopeSensor(cfg EnvelopeSensorConfig) *EnvelopeSensor {
	if cfg.WindowReads == 0 {
		cfg.WindowReads = 400
	}
	if cfg.Horizon == 0 {
		cfg.Horizon = 10
	}
	if cfg.FreezeReads == 0 {
		fr := int64(8)
		if ms40 := 1920 / cfg.ReaderFrames; ms40 > fr { // 40 ms at 48 kHz
			fr = ms40
		}
		cfg.FreezeReads = fr
	}
	return &EnvelopeSensor{cfg: cfg}
}

func (s *EnvelopeSensor) OnWrite(time, pos, frames int64) {}

func (s *EnvelopeSensor) OnRead(o Obs) {
	raw := o.Delivered - o.Tick*s.cfg.ReaderFrames
	if s.cfg.UseRawFill {
		if o.Fill < 0 {
			return // raw-fill sensing needs an attached buffer
		}
		raw = o.Fill
	}

	delta := o.Delivered - s.prevDelivered
	s.prevDelivered = o.Delivered
	if delta > 0 {
		if s.frozen {
			// Resume: excise whatever the frozen span did to the signal.
			s.anchor += raw - s.lastLevel
			s.frozen = false
		}
		s.gapRun = 0
		s.lastLevel = raw
	} else {
		s.gapRun++
		if !s.frozen && s.gapRun > s.cfg.FreezeReads {
			s.frozen = true
		}
	}
	if s.frozen {
		return
	}

	s.curTick = o.Tick
	v := raw - s.anchor
	s.push(v)
}

func (s *EnvelopeSensor) push(v int64) {
	i := s.pushes
	s.pushes++
	for len(s.maxDQ) > 0 && s.maxDQ[len(s.maxDQ)-1].v <= v {
		s.maxDQ = s.maxDQ[:len(s.maxDQ)-1]
	}
	s.maxDQ = append(s.maxDQ, tickVal{i, v})
	for len(s.minDQ) > 0 && s.minDQ[len(s.minDQ)-1].v >= v {
		s.minDQ = s.minDQ[:len(s.minDQ)-1]
	}
	s.minDQ = append(s.minDQ, tickVal{i, v})
	// Prune to the window.
	lo := s.pushes - s.cfg.WindowReads
	for len(s.maxDQ) > 0 && s.maxDQ[0].idx < lo {
		s.maxDQ = s.maxDQ[1:]
	}
	for len(s.minDQ) > 0 && s.minDQ[0].idx < lo {
		s.minDQ = s.minDQ[1:]
	}

	s.winSum += float64(v)
	s.winBuf = append(s.winBuf, v)
	s.winN++
	if s.winN < s.cfg.WindowReads {
		return
	}
	s.closeWindow()
}

func (s *EnvelopeSensor) closeWindow() {
	winMax := s.maxDQ[0].v
	winMin := s.minDQ[0].v
	mean := s.winSum / float64(s.winN)
	s.sortBuf = append(s.sortBuf[:0], s.winBuf...)
	slices.Sort(s.sortBuf)
	median := s.sortBuf[len(s.sortBuf)/2]
	s.winSum, s.winN = 0, 0
	s.winBuf = s.winBuf[:0]

	s.means = append(s.means, mean)
	if int64(len(s.means)) > s.cfg.Horizon {
		s.means = s.means[1:]
	}
	ppm := 0.0
	if n := len(s.means); n >= 2 {
		perRead := (s.means[n-1] - s.means[0]) / float64(int64(n-1)*s.cfg.WindowReads)
		ppm = perRead / float64(s.cfg.ReaderFrames) * 1e6
	}

	s.ests = append(s.ests, Estimate{
		Tick:      s.curTick,
		Ref:       winMax,
		Mean:      mean,
		Median:    median,
		WidthLow:  median - winMin,
		WidthHigh: winMax - median,
		DriftPPM:  ppm,
		Frozen:    s.frozen,
	})
}

func (s *EnvelopeSensor) Estimates() []Estimate { return s.ests }

// TimestampSensor is the harness-only arrival-domain reference: per-write
// transit deviation dev = arrivalTime − streamPos (master frames; drift
// appears as dev's slope, lateness as its spread above the windowed min).
// Immune to reader behaviour, actuation, and — elegantly — to loss and
// outage: lost positions and lost time cancel in dev. Windows tumble by
// write count.
type TimestampSensor struct {
	windowWrites int64
	horizon      int64

	writes int64
	maxDQ  []tickVal
	minDQ  []tickVal
	winN   int64

	mins []tickVal // (pos, winMinDev) ring for drift, cap horizon
	ests []Estimate
}

// NewTimestampSensor builds the reference sensor; windowWrites ≈ one
// fill-sensor window's worth of packets.
func NewTimestampSensor(windowWrites int64) *TimestampSensor {
	if windowWrites == 0 {
		windowWrites = 400
	}
	return &TimestampSensor{windowWrites: windowWrites, horizon: 10}
}

func (s *TimestampSensor) OnRead(o Obs) {}

func (s *TimestampSensor) OnWrite(time, pos, frames int64) {
	dev := time - pos
	i := s.writes
	s.writes++
	for len(s.maxDQ) > 0 && s.maxDQ[len(s.maxDQ)-1].v <= dev {
		s.maxDQ = s.maxDQ[:len(s.maxDQ)-1]
	}
	s.maxDQ = append(s.maxDQ, tickVal{i, dev})
	for len(s.minDQ) > 0 && s.minDQ[len(s.minDQ)-1].v >= dev {
		s.minDQ = s.minDQ[:len(s.minDQ)-1]
	}
	s.minDQ = append(s.minDQ, tickVal{i, dev})
	lo := s.writes - s.windowWrites
	for len(s.maxDQ) > 0 && s.maxDQ[0].idx < lo {
		s.maxDQ = s.maxDQ[1:]
	}
	for len(s.minDQ) > 0 && s.minDQ[0].idx < lo {
		s.minDQ = s.minDQ[1:]
	}

	s.winN++
	if s.winN < s.windowWrites {
		return
	}
	s.winN = 0

	winMin := s.minDQ[0].v
	winMax := s.maxDQ[0].v
	s.mins = append(s.mins, tickVal{pos, winMin})
	if int64(len(s.mins)) > s.horizon {
		s.mins = s.mins[1:]
	}
	ppm := 0.0
	if n := len(s.mins); n >= 2 {
		posSpan := s.mins[n-1].idx - s.mins[0].idx
		if posSpan > 0 {
			// dev slope per stream frame ≈ −ε for a writer fast by ε.
			ppm = -float64(s.mins[n-1].v-s.mins[0].v) / float64(posSpan) * 1e6
		}
	}
	s.ests = append(s.ests, Estimate{
		Tick:     pos,
		Ref:      winMin,
		WidthLow: winMax - winMin,
		DriftPPM: ppm,
	})
}

func (s *TimestampSensor) Estimates() []Estimate { return s.ests }
