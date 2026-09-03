package engine

import (
	"time"

	"github.com/panaudia/panaudia-lasa/engine/ambisonic"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

// The render loop keeps the incumbent's load-bearing phase structure:
// In → barrier → Across → barrier → Out. The In/Across barrier is
// correctness, not just scheduling — every listener's fractional-ITD
// reads (and pose-fresh positions) must see completed input rings and
// settled positions. Workers own their mixer scratch (two dry buses +
// reverb), so Across jobs run concurrently without sharing state.

const (
	phaseIn = iota
	phaseAcross
	phaseOut
)

// A job is one entity's work in one phase. Deliberately fine-grained:
// chunked dispatch (one job per worker per phase) was tried on
// 2026-07-30 and MEASURED SLOWER on the capacity sweep — the park/
// unpark churn it removed was off the critical path, while per-entity
// granularity provides load balancing the barrier actually waits on.
// Don't re-chunk without the sweep saying otherwise.
type job struct {
	e     *entity
	phase int
}

type worker struct {
	mixer       *ambisonic.Mixer
	mixerR      *ambisonic.Mixer
	reverbMixer *ambisonic.Mixer
	jobs        chan job
}

func (m *Mixer) newWorker() *worker {
	reverbCfg := m.encCfg
	reverbCfg.ChannelCount = common.REVERB_CHANNELS
	reverbCfg.Order = common.OrderForChannelCount(common.REVERB_CHANNELS)

	w := &worker{
		mixer:       ambisonic.NewMixer(m.encCfg),
		mixerR:      ambisonic.NewMixer(m.encCfg),
		reverbMixer: ambisonic.NewMixer(reverbCfg),
		jobs:        make(chan job, 256),
	}
	go func() {
		for j := range w.jobs {
			m.runJob(w, j)
			m.wg.Done()
		}
	}()
	return w
}

// runPhase fans a phase out one job per entity (see the job comment for
// why not chunks) and waits for the barrier.
func (m *Mixer) runPhase(ents []*entity, phase int) {
	if len(ents) == 0 {
		return
	}
	for _, e := range ents {
		m.wg.Add(1)
		m.workers[m.nextWork].jobs <- job{e: e, phase: phase}
		m.nextWork = (m.nextWork + 1) % len(m.workers)
	}
	m.wg.Wait()
}

func (m *Mixer) runJob(w *worker, j job) {
	switch j.phase {
	case phaseIn:
		j.e.phaseIn(m.shared)
	case phaseAcross:
		if j.e.sink != nil {
			j.e.enc.EncodePeers(j.e.peers, w.mixer, w.mixerR, w.reverbMixer)
		}
		if j.e.ambiEnc != nil {
			// The raw-ambisonic render: classic single bus, world-frame.
			// The worker's mixers are per-job scratch (EncodePeers resets
			// them), so the second pass reuses them sequentially.
			j.e.ambiEnc.EncodePeers(j.e.peers, w.mixer, nil, w.reverbMixer)
		}
	case phaseOut:
		if j.e.sink != nil {
			j.e.enc.PostMix()
			j.e.sink.emit(j.e.enc.Output)
		}
		if j.e.ambiEnc != nil {
			j.e.ambiEnc.PostMix()
			for _, k := range j.e.ambiSinks {
				k.emit(j.e.ambiEnc.Output)
			}
		}
	}
}

// phaseIn reads one frame of audio and applies this tick's poses.
// Source role: the frame and the pose of its last sample come from the
// jitter-aligned pipeline together. Sink role: rotation and listener
// position are read once from the latest-wins slot — fresh, and the
// single read means the bilateral encode and the binaural render can
// never disagree within a frame. Runs on a worker; everything touched is
// entity-confined, and the In/Across barrier publishes it.
func (e *entity) phaseIn(shared *ambisonic.SharedInputs) {
	if e.src != nil {
		p, ok := e.src.readFrame(e.enc.Input)
		e.enc.PushInputRing()
		if ok {
			e.enc.SetPosition(p.position())
			// Source facing feeds the directivity law; skipped for omni
			// sources (the law ignores it and the matrix build is the
			// only per-tick cost).
			if e.params.Directivity > 0 {
				e.enc.SetSourceForward(p.forward())
			}
		}
		// Presence loudness: measured from the frame actually rendered,
		// post-gain (design §2).
		e.src.updateRenderLoudness(e.enc.Input, e.params.Gain)
		// Publish this frame to the shared slot matrix (own row only;
		// the In/Across barrier makes it visible to every reverb GEMM).
		shared.SetRow(e.slot, e.enc.Input)
	}
	if e.sink != nil {
		if p, ok := e.sink.pose.load(); ok {
			e.enc.SetRotation(p.rotation())
			e.enc.SetListenerPosition(p.position())
		}
	}
	if e.ambiEnc != nil {
		// World-frame field: position only, rotation never (decision 10).
		// Any of the entity's pose slots serves — they carry the same
		// ingress fan-out; prefer the binaural sink's when present.
		if p, ok := e.listenerPose(); ok {
			e.ambiEnc.SetListenerPosition(p.position())
		}
	}
}

// listenerPose returns the freshest available listening pose across the
// entity's sink halves (they all receive the same ingress fan-out).
func (e *entity) listenerPose() (Pose, bool) {
	if e.sink != nil {
		if p, ok := e.sink.pose.load(); ok {
			return p, true
		}
	}
	for _, k := range e.ambiSinks {
		if p, ok := k.pose.load(); ok {
			return p, true
		}
	}
	return Pose{}, false
}

// Process runs one 5 ms frame: apply queued changes, then the three
// phases. Call from exactly one goroutine — the audio thread. Never
// blocks on ingest, network, or state. Per-phase timing accumulates
// into Stats (a few time.Now calls per tick — noise against the frame).
func (m *Mixer) Process() {
	m.tickCount++
	// The global sample clock (design §1): this tick's frame spans
	// samples [frameStart, frameStart+240). Written here, before any
	// dispatch, so workers read it race-free through the phase barriers.
	m.frameStart = uint64(m.tickCount-1) * FrameSize
	m.counters.ticks.Add(1)
	t0 := time.Now()

	m.drainChanges()
	if m.audibleDirty {
		m.recomputeAudible()
	}
	t1 := time.Now()
	m.counters.changes.Add(int64(t1.Sub(t0)))

	// Lockstep sets read their one N-channel frame here, before the
	// parallel In phase, so every member's readFrame finds it ready.
	for _, g := range m.groups {
		g.read()
	}
	m.sourceEntities = m.sourceEntities[:0]
	m.allEnts = m.allEnts[:0]
	m.sinkEnts = m.sinkEnts[:0]
	for _, e := range m.slots {
		if e == nil {
			continue
		}
		m.allEnts = append(m.allEnts, e)
		if e.src != nil {
			m.sourceEntities = append(m.sourceEntities, e)
		}
		if e.listening() {
			m.sinkEnts = append(m.sinkEnts, e)
		}
	}
	// Each listener's peers row: its channel-audible subset of this
	// tick's sources (option D — include/exclude by construction).
	for _, e := range m.sinkEnts {
		e.peers = e.peers[:0]
		row := m.audible[e.slot]
		for _, s := range m.sourceEntities {
			if row[s.slot] {
				e.peers = append(e.peers, s.enc)
			}
		}
	}
	t2 := time.Now()
	m.counters.prep.Add(int64(t2.Sub(t1)))

	m.runPhase(m.allEnts, phaseIn)
	t3 := time.Now()
	m.counters.in.Add(int64(t3.Sub(t2)))

	m.runPhase(m.sinkEnts, phaseAcross)
	t4 := time.Now()
	m.counters.across.Add(int64(t4.Sub(t3)))

	m.runPhase(m.sinkEnts, phaseOut)
	m.counters.out.Add(int64(time.Since(t4)))
}

// Ticker paces Run; satisfied by the copied timing.Ticker. Tick blocks
// until the next frame boundary and returns how long the frame's work
// took.
type Ticker interface {
	Tick() time.Duration
}

// Run drives Process under t until Close. The standalone-server
// convenience; a host with its own clock calls Process directly. Run
// announces itself so Close can wait for the loop to finish its last
// frame before draining audio-thread state.
func (m *Mixer) Run(t Ticker) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	done := make(chan struct{})
	m.runDone = done
	m.mu.Unlock()
	defer close(done)
	for {
		select {
		case <-m.stop:
			return
		default:
		}
		m.Process()
		t.Tick()
	}
}
