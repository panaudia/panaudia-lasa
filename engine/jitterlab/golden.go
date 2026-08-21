package jitterlab

import "fmt"

// golden.go — the golden scenario suite: one named scenario per
// documented field incident plus synthetic basics
// (plan/jitter-v4-harness.md, "golden scenario suite"; field evidence in
// plan/jitter-v3-burst-aware-widening.md). Parameters follow the field
// observations; durations are chosen so the slow phenomena (drift
// equilibria) have time to express. All are deterministic per seed.

// Suite durations, in seconds of virtual time.
const (
	goldenShortS = 60
	goldenMedS   = 120
	goldenLongS  = 600 // drift scenarios: 21 ppm needs minutes to park
)

func secs(g Geometry, s int64) int64 { return s * int64(g.SampleRate) }

// BrowserWriterClumps: worker-paced sends arriving in ~10–15 ms clumps
// (2026-08-05 server-ingest incident): near-continuous pause-flush.
func BrowserWriterClumps(g Geometry, seed uint64) Scenario {
	dur := secs(g, goldenMedS)
	ms := g.MsToFrames
	stalls := GenStalls(seed, dur, ms(10), ms(15), ms(5), ms(10), ms(5), ms(10), false)
	return Build("browser-writer-clumps", g, dur,
		WriterModel{BaseDelay: ms(2), Stalls: stalls, Seed: seed}, ReaderModel{})
}

// WorkerGC: 10–33 ms event-loop pauses whose frequency grows with
// session age (2026-08-06): pause-flush with a shrinking gap.
func WorkerGC(g Geometry, seed uint64) Scenario {
	dur := secs(g, 300)
	ms := g.MsToFrames
	stalls := GenStalls(seed, dur, ms(10), ms(33),
		ms(1500), ms(2500), ms(200), ms(400), false)
	return Build("worker-gc", g, dur,
		WriterModel{BaseDelay: ms(2), Stalls: stalls, Seed: seed}, ReaderModel{})
}

// BluetoothReader: output-clocked reader with ~30 ms callback clumps
// (AirPods A2DP, 2026-08-06). The writer is clean; the reader stalls and
// then reads back-to-back.
func BluetoothReader(g Geometry, seed uint64) Scenario {
	dur := secs(g, goldenMedS)
	ms := g.MsToFrames
	stalls := GenStalls(seed, dur, ms(25), ms(35), ms(80), ms(160), ms(80), ms(160), false)
	return Build("bluetooth-reader", g, dur,
		WriterModel{BaseDelay: ms(2), Seed: seed}, ReaderModel{Stalls: stalls})
}

// DACDrift: writer clock offset in ppm against a clean network
// (2026-08-07 bridge/Scarlett: ~21 ppm parked fill at the drop line).
// Positive ppm = fast writer = fill climbs.
func DACDrift(g Geometry, ppm float64) Scenario {
	return Build(fmt.Sprintf("dac-drift%+g", ppm), g, secs(g, goldenLongS),
		WriterModel{PPM: ppm, BaseDelay: g.MsToFrames(2)}, ReaderModel{})
}

// TickerCatchup: a single host stall with full catch-up (the server's
// accumulated-time ticker sends the backlog in one burst). stallMs sized
// to land the post-flush fill ABOVE the overrun line: v3 recovers with
// one snap.
func TickerCatchup(g Geometry, stallMs float64) Scenario {
	dur := secs(g, goldenShortS)
	return Build("ticker-catchup", g, dur,
		WriterModel{
			BaseDelay: g.MsToFrames(2),
			Stalls:    []Stall{{Start: dur / 2, Dur: g.MsToFrames(stallMs)}},
		}, ReaderModel{})
}

// TickerCatchupSmall: a stall small enough that the post-flush fill
// lands inside the band rather than over the overrun line. Phase-2
// finding (2026-08-11): in the W = R geometry v3 RECOVERS from this —
// the stall's one underrun read is a free whole-packet time-slip
// (rp frozen while wall time passes), and a ~0.7 s drop-splice drip
// drains the rest, ending at the pre-stall time-domain latency. Cost per
// stall: one silence click + ~150–240 drop splices. The permanent
// in-band-offset pathology appears under DRIFT (see DACDrift +21:
// parked at the drop line indefinitely), not under one-shot stalls in
// this geometry.
func TickerCatchupSmall(g Geometry) Scenario {
	dur := secs(g, goldenShortS)
	return Build("ticker-catchup-small", g, dur,
		WriterModel{
			BaseDelay: g.MsToFrames(2),
			Stalls:    []Stall{{Start: dur / 2, Dur: g.MsToFrames(20)}},
		}, ReaderModel{})
}

// DeviceStall: a real-time source stalls and never catches up — the
// stalled interval's audio is lost (stall-and-drop).
func DeviceStall(g Geometry) Scenario {
	dur := secs(g, goldenShortS)
	return Build("device-stall", g, dur,
		WriterModel{
			BaseDelay: g.MsToFrames(2),
			Stalls:    []Stall{{Start: dur / 2, Dur: g.MsToFrames(200), Drop: true}},
		}, ReaderModel{})
}

// EncodeSweepTail: the last channels of an encode sweep inherit the
// cycle's accumulated timing variance (2026-08-07 ch15/16 finding) —
// one-sided lateness spikes on a rate-correct stream.
func EncodeSweepTail(g Geometry, seed uint64) Scenario {
	return Build("encode-sweep-tail", g, secs(g, goldenMedS),
		WriterModel{BaseDelay: g.MsToFrames(2), OneSided: g.MsToFrames(3), Seed: seed}, ReaderModel{})
}

// OutageResume: a multi-second network outage; audio sent during it is
// lost; the stream resumes (design_v3 §1: must not inflate latency).
func OutageResume(g Geometry) Scenario {
	dur := secs(g, goldenShortS)
	return Build("outage-resume", g, dur,
		WriterModel{
			BaseDelay: g.MsToFrames(2),
			Outages:   []Stall{{Start: dur / 3, Dur: secs(g, 3), Drop: true}},
		}, ReaderModel{})
}

// LossRandom: Bernoulli packet loss on an otherwise calm stream
// (design_v3 §1: conceal upstream, never add latency — the buffer sees
// missing stream positions).
func LossRandom(g Geometry, lossP float64, seed uint64) Scenario {
	return Build("loss-random", g, secs(g, goldenMedS),
		WriterModel{BaseDelay: g.MsToFrames(2), LossP: lossP, Seed: seed}, ReaderModel{})
}

// DriftPlusClumps: the killer composite (the 2026-08-06 grit incident's
// shape): writer-fast drift parks fill high, and reader-side callback
// clumps (the incident was an output-clocked playout buffer) repeatedly
// push it over the top — zero-headroom operation, continuous splices.
func DriftPlusClumps(g Geometry, seed uint64) Scenario {
	dur := secs(g, goldenLongS)
	ms := g.MsToFrames
	stalls := GenStalls(seed, dur, ms(5), ms(20), ms(300), ms(700), ms(300), ms(700), false)
	return Build("drift+clumps", g, dur,
		WriterModel{PPM: 21, BaseDelay: ms(2), Seed: seed}, ReaderModel{Stalls: stalls})
}

// OnsetAfterCalm: a long calm stretch (the adaptive band narrows to its
// minima), then jitter begins — tests the knife-edge at jitter onset
// (design_v3 §10, L_min discussion).
func OnsetAfterCalm(g Geometry, seed uint64) Scenario {
	dur := secs(g, 240)
	return Build("onset-after-calm", g, dur,
		WriterModel{
			BaseDelay:   g.MsToFrames(2),
			JitterSigma: g.MsToFrames(3),
			JitterFrom:  dur / 2,
			Seed:        seed,
		}, ReaderModel{})
}

// GoldenSuite returns the full scenario table for one geometry.
func GoldenSuite(g Geometry, seed uint64) []Scenario {
	return []Scenario{
		Calm(g, secs(g, goldenShortS), g.MsToFrames(2.5)),
		IIDJitter(g, secs(g, goldenMedS), g.MsToFrames(5), g.MsToFrames(4), seed),
		BrowserWriterClumps(g, seed),
		WorkerGC(g, seed),
		BluetoothReader(g, seed),
		DACDrift(g, 21),
		DACDrift(g, -21),
		DACDrift(g, 200),
		DACDrift(g, -200),
		TickerCatchup(g, 200),
		TickerCatchupSmall(g),
		DeviceStall(g),
		EncodeSweepTail(g, seed),
		OutageResume(g),
		LossRandom(g, 0.02, seed),
		DriftPlusClumps(g, seed),
		OnsetAfterCalm(g, seed),
	}
}

// FECLoss: Bernoulli loss with LASA repeated-frame FEC seen through the
// transport-side reassembler (repeat lag in packets). Each loss becomes
// a bounded hold-and-flush in the delivery; only double losses (both
// copies) leave stream gaps. A9's scenario: a declared floor ≥ the
// repeat lag absorbs every hold silently; a violated floor clicks until
// the width estimator learns what the declaration would have
// pre-provisioned.
func FECLoss(g Geometry, lossP float64, lagPackets int64, seed uint64) Scenario {
	return Build(fmt.Sprintf("fec-loss-lag%d", lagPackets), g, secs(g, goldenMedS),
		WriterModel{BaseDelay: g.MsToFrames(2), LossP: lossP, FECLag: lagPackets, Seed: seed},
		ReaderModel{})
}
