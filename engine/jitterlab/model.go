package jitterlab

// model.go — the phase-2 generator pipeline (plan/jitter-v4-harness.md,
// "writer-side event generation"). A WriterModel/ReaderModel pair plus a
// geometry builds a Scenario: packet k is sent at k·W on the writer's
// clock (mapped to master time through its ppm offset), passes through
// composable delay/loss stages, and arrives FIFO-clamped. Events carry
// their stream positions explicitly so that lost packets (stream advances,
// nothing delivered) can never corrupt position accounting regardless of
// event ordering.
//
// Loss semantics: per design_v3 §1, lost data never arrives — the buffer
// receives a stream with missing positions (time-compressed content), not
// upstream concealment. The runner records the gaps and reclassifies
// decoder snaps that exactly span unwritten positions as ArtifactLossGap
// (upstream loss, not buffer behaviour).

import (
	"math"
	"math/rand/v2"
)

// Stall is a half-open interval [Start, Start+Dur) of master time during
// which a side is paused. For writers: arrivals due in the interval are
// held and flushed together at its end (pause-flush), or lost outright
// when Drop is set (stall-and-drop — a real-time source that never
// catches up). For readers: reads due in the interval run back-to-back at
// its end (callback clumping). Stall lists must be sorted and
// non-overlapping.
type Stall struct {
	Start, Dur int64
	Drop       bool
}

// End returns the first master-time frame after the stall.
func (s Stall) End() int64 { return s.Start + s.Dur }

// WriterModel describes the send process and the network between source
// and buffer. Zero value = ideal: on-time, zero-delay, lossless.
type WriterModel struct {
	PPM float64 // writer clock offset, parts per million (+ = fast writer)

	BaseDelay   int64 // constant transit delay, frames
	JitterSigma int64 // iid gaussian delay component, frames
	JitterFrom  int64 // master time before which JitterSigma is inactive
	OneSided    int64 // scale of |N(0,1)|·OneSided one-sided lateness (encode-sweep tail)

	StepAt    int64 // master time of a permanent path-delay change (0 = none)
	StepDelta int64 // frames added to delay for packets sent at/after StepAt

	LossP   float64 // Bernoulli loss probability per packet
	Stalls  []Stall // pause-flush (or stall-and-drop when Drop) intervals
	Outages []Stall // send-time windows in which packets are lost (Drop implied)

	// FECLag models LASA repeated-frame FEC as seen through the
	// transport-side reassembler (harness doc, "FEC repeat stage";
	// design-space doc, use-spectrum section): packet k's repeat is sent
	// FECLag packets later and travels independently (fresh delay draw,
	// same loss probability, same outage windows). The reassembler
	// delivers strictly in order — a lost original's successors are HELD
	// until the first surviving copy arrives, then flushed together; a
	// frame whose copies are all lost (or arrive past the give-up
	// deadline, repeat-send + base delay + 2 W) is CONCEALED: the
	// reassembler emits a zero-content frame for the position at the
	// deadline and successors flush behind it. The buffer therefore
	// sees exactly the design's contract: an in-order, gap-free stream
	// with bounded stall+flush delivery on loss. 0 = FEC off. The
	// oracle needs no amendment: repaired frames enter the schedule at
	// their actual (repeat-driven) delivery times, concealed frames at
	// the give-up deadline — the reassembler-as-delivery-stage model
	// makes the "credit repairs at repeat arrival" rule automatic.
	FECLag int64

	Seed uint64 // rng seed for jitter and loss draws
}

// ReaderModel describes the consumer's callback clock.
type ReaderModel struct {
	PPM    float64 // reader clock offset, ppm (+ = fast reader)
	Stalls []Stall // callback stalls; deferred reads run back-to-back at End
}

// clockTime maps the k-th tick of a period-`step` clock with the given
// ppm offset onto the master clock. A fast clock (+ppm) ticks early.
func clockTime(k, step int64, ppm float64) int64 {
	return int64(math.Round(float64(k*step) / (1.0 + ppm*1e-6)))
}

// inStall returns the first stall in the sorted list containing t, or nil.
func inStall(stalls []Stall, t int64) *Stall {
	for i := range stalls {
		if t < stalls[i].Start {
			return nil
		}
		if t < stalls[i].End() {
			return &stalls[i]
		}
	}
	return nil
}

// Build materializes a scenario from the models. Deterministic for a
// given (models, geometry, duration).
func Build(name string, g Geometry, dur int64, wm WriterModel, rm ReaderModel) Scenario {
	rng := rand.New(rand.NewPCG(wm.Seed, 0x6a177e2))

	// oneArrival runs one packet copy through the delay/loss stages:
	// delay draws, outage window at its send time, Bernoulli loss,
	// pause-flush/stall-and-drop. Returns (arrival, lost). Draws happen
	// for every call so the rng sequence depends only on call order,
	// keeping runs deterministic per seed.
	oneArrival := func(send int64) (int64, bool) {
		delay := wm.BaseDelay
		if wm.JitterSigma > 0 && send >= wm.JitterFrom {
			delay += int64(math.Round(rng.NormFloat64() * float64(wm.JitterSigma)))
		}
		if wm.OneSided > 0 {
			delay += int64(math.Round(math.Abs(rng.NormFloat64()) * float64(wm.OneSided)))
		}
		if wm.StepDelta != 0 && send >= wm.StepAt {
			delay += wm.StepDelta
		}
		if delay < 0 {
			delay = 0
		}
		lost := inStall(wm.Outages, send) != nil
		if !lost && wm.LossP > 0 && rng.Float64() < wm.LossP {
			lost = true
		}
		arr := send + delay
		if !lost {
			// A flushed arrival may land in a later stall; re-check.
			for {
				s := inStall(wm.Stalls, arr)
				if s == nil {
					break
				}
				if s.Drop {
					lost = true
					break
				}
				arr = s.End()
			}
		}
		return arr, lost
	}

	// Writer: packets in send order; arrivals FIFO-clamped (the
	// transport delivers in order). A packet that cannot arrive inside
	// the scenario (lost, or arrival ≥ dur) becomes a KindLost event at
	// its send time: the stream position advances, nothing is delivered.
	// With FECLag set, the in-order rule is instead enforced by the
	// reassembler pass below, which also repairs from repeats.
	var writes []Event
	var pos int64
	prevArr := int64(0)
	for k := int64(0); ; k++ {
		send := clockTime(k, g.WriterFrames, wm.PPM)
		if send >= dur {
			break
		}
		arr, lost := oneArrival(send)

		if wm.FECLag > 0 {
			// Reassembler path: the frame is available at its first
			// surviving copy's arrival, or dead. The give-up deadline is
			// the repeat's expected arrival plus 2 W of margin; a copy
			// arriving later finds the reassembler already flushed past
			// it (equivalent to dead).
			repSend := clockTime(k+wm.FECLag, g.WriterFrames, wm.PPM)
			repArr, repLost := oneArrival(repSend)
			giveUp := repSend + wm.BaseDelay + 2*g.WriterFrames
			ready := int64(math.MaxInt64)
			if !lost {
				ready = arr
			}
			if !repLost && repArr < ready {
				ready = repArr
			}
			if ready <= giveUp {
				rel := max(prevArr, ready) // held behind unresolved predecessors
				if rel < dur {
					prevArr = rel
					writes = append(writes, Event{Time: rel, Kind: KindWrite, Frames: g.WriterFrames, Pos: pos})
				} else {
					writes = append(writes, Event{Time: send, Kind: KindLost, Frames: g.WriterFrames, Pos: pos})
				}
			} else {
				// Dead: at the give-up deadline the reassembler emits a
				// CONCEALMENT frame for the position (the design-space
				// FEC choice: it already delayed for the repair window;
				// packet-granular PLC is its natural role). The stream
				// stays continuous — critically, this keeps the death out
				// of the buffer's fill/virtual views, where it would be
				// indistinguishable from a drift-down quantum and would
				// be wrongly "repaid" with synthetic inserts (missing
				// content and a slow clock need OPPOSITE responses; only
				// the reassembler has the sequence numbers to tell them
				// apart — found in A9 bring-up as a fill sag + insert
				// grind). Successors flush behind it.
				rel := max(prevArr, giveUp)
				if rel < dur {
					prevArr = rel
					writes = append(writes, Event{Time: rel, Kind: KindWrite, Frames: g.WriterFrames, Pos: pos, Conceal: true})
				} else {
					writes = append(writes, Event{Time: send, Kind: KindLost, Frames: g.WriterFrames, Pos: pos})
				}
			}
			pos += g.WriterFrames
			continue
		}

		if !lost && arr < prevArr {
			arr = prevArr // FIFO
		}
		if !lost && arr >= dur {
			lost = true // never delivered within the scenario
		}
		if lost {
			writes = append(writes, Event{Time: send, Kind: KindLost, Frames: g.WriterFrames, Pos: pos})
		} else {
			prevArr = arr
			writes = append(writes, Event{Time: arr, Kind: KindWrite, Frames: g.WriterFrames, Pos: pos})
		}
		pos += g.WriterFrames
	}

	// Reader: periodic on its own clock; stalled reads run at stall end.
	var reads []Event
	for j := int64(0); ; j++ {
		t := clockTime(j, g.ReaderFrames, rm.PPM)
		if t >= dur {
			break
		}
		if s := inStall(rm.Stalls, t); s != nil {
			t = s.End()
			if t >= dur {
				break
			}
		}
		reads = append(reads, Event{Time: t, Kind: KindRead, Frames: g.ReaderFrames})
	}

	return Scenario{
		Name:      name,
		Geometry:  g,
		Duration:  dur,
		Events:    mergeEvents(writes, reads),
		WriterPPM: wm.PPM,
	}
}

// GenStalls builds a sorted, non-overlapping stall list covering [0, dur):
// stall durations uniform in [durMin, durMax]; the gap from one stall's
// end to the next stall's start is uniform in a range that interpolates
// linearly from [gapMin0, gapMax0] at t=0 to [gapMin1, gapMax1] at t=dur
// (equal ranges give a stationary process; a shrinking range models e.g.
// GC frequency growing with session age).
func GenStalls(seed uint64, dur, durMin, durMax, gapMin0, gapMax0, gapMin1, gapMax1 int64, drop bool) []Stall {
	rng := rand.New(rand.NewPCG(seed, 0x57a115))
	uniform := func(lo, hi int64) int64 {
		if hi <= lo {
			return lo
		}
		return lo + rng.Int64N(hi-lo+1)
	}
	var out []Stall
	t := int64(0)
	for {
		frac := float64(t) / float64(dur)
		gLo := gapMin0 + int64(frac*float64(gapMin1-gapMin0))
		gHi := gapMax0 + int64(frac*float64(gapMax1-gapMax0))
		t += uniform(gLo, gHi)
		if t >= dur {
			return out
		}
		d := uniform(durMin, durMax)
		out = append(out, Stall{Start: t, Dur: d, Drop: drop})
		t += d
	}
}
