// Package engine is the protocol- and transport-blind spatial audio
// engine: jitter buffering, decode, spatial render, policy, binaural
// encode. It composes the copied DSP packages (Layer 1) under a fresh
// orchestration (Layer 2) — see PROVENANCE.md.
//
// The engine has no wall-clock: its only time bases are packet sequence
// numbers, stream sample indices, and the host's tick. All mutation of
// render state happens on the audio thread (the goroutine calling
// Process), fed through an internal changes queue; ingest and pose
// writes are lock-free and never touch the render structures.
package engine

import (
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/panaudia/panaudia-lasa/engine/ambisonic"
	"github.com/panaudia/panaudia-lasa/engine/binaural"
	"github.com/panaudia/panaudia-lasa/engine/buffers"
	"github.com/panaudia/panaudia-lasa/engine/common"
	"github.com/panaudia/panaudia-lasa/engine/convolver"
	"github.com/panaudia/panaudia-lasa/engine/hrtf"
	"github.com/panaudia/panaudia-lasa/engine/inout"

	"sync"
)

var installDefaultDelayModel sync.Once

var (
	ErrClosed          = errors.New("engine: mixer closed")
	ErrDuplicateSource = errors.New("engine: entity already has a source")
	ErrDuplicateSink   = errors.New("engine: entity already has a sink")
	ErrFull            = errors.New("engine: entity capacity reached")
	ErrUnknownEntity   = errors.New("engine: unknown entity id")
)

// entity is one participant: an optional source (audio in) and an
// optional sink (rendered audio out) sharing a string id, a slot, and an
// ambisonic encoder. Audio-thread-only.
type entity struct {
	id     string
	uid    uuid.UUID // Layer 1's identity token, fresh per entity instance
	slot   int
	enc    *ambisonic.Encoder
	src    *Source
	sink   *Sink
	params RenderParams
	// The raw-ambisonic half (ambisink.go): one shared classic
	// world-frame encoder plus a sink per subscribed ambi order.
	ambiEnc   *ambisonic.Encoder
	ambiSinks map[int]*AmbiSink
	// peers is this listener's channel-filtered source list, assembled
	// each tick from the audibility matrix and handed to EncodePeers —
	// include/exclude policy by construction (channels.go).
	peers []*ambisonic.Encoder
}

// listening reports whether the entity renders anything this tick.
func (e *entity) listening() bool { return e.sink != nil || e.ambiEnc != nil }

// regEntry is the control-plane view of an entity, guarded by Mixer.mu:
// duplicate checks and capacity live here so Add/Remove can answer
// synchronously while the audio-side structures stay thread-confined.
type regEntry struct {
	src  *Source
	sink *Sink
	ambi map[int]*AmbiSink
}

// Mixer renders one space. Construct with New; drive with Process (host
// clock) or Run (self-clocked over a Ticker).
type Mixer struct {
	cfg          Config
	channelCount int
	encCfg       common.MixerConfig
	convolverSet *convolver.Set
	// shared is the slot-indexed source frame matrix: written by
	// sources in the In phase (disjoint rows), read by every listener's
	// reverb GEMM in Across — the phase barrier is the synchronization.
	shared *ambisonic.SharedInputs

	mu          sync.Mutex
	reg         map[string]*regEntry
	entityCount int
	closed      bool

	changes chan func()

	// Audio-thread state.
	ents           map[string]*entity
	slots          []*entity
	sourceEntities []*entity
	allEnts        []*entity // per-tick phase scratch (chunked dispatch)
	sinkEnts       []*entity
	channelTable   map[string]ChannelParams
	// The pair record, [listenerSlot][sourceSlot]: audibility plus the
	// per-pair scalars (composed gain factor, attenuation exponent),
	// recomputed whole when audibleDirty (channels.go). Row slices are
	// stable — each listener's encoder holds its row pointers.
	audible      [][]bool
	pairGain     [][]float32
	pairAttExp   [][]float64
	audibleDirty bool
	// Personal mute/solo (§6), keyed by entity ID so preferences match
	// across the target's departures and rejoins. Audio-thread state,
	// consumed by resolvePair; the owning entity's entries die with it
	// (removeEntity), mirroring the owner's connection-scoped sweep.
	personalMutes map[string]map[string]bool
	personalSolos map[string]map[string]bool

	workers   []*worker
	nextWork  int
	wg        sync.WaitGroup
	stop      chan struct{}
	stopOnce  sync.Once
	runDone   chan struct{} // non-nil once Run started; closed when it returns
	tickCount int64
	// frameStart is the global sample clock: the first sample index of
	// the frame being rendered this tick ((tick−1)×240). Set at the top
	// of Process; read by Out-phase workers for sink frame stamps.
	frameStart uint64
	counters   processCounters
}

// New builds a Mixer. Config values are taken literally — start from
// DefaultConfig. The HRTF set, its delay model, and the shared convolver
// filter set are installed here, exactly as the incumbent backend did.
func New(cfg Config) (*Mixer, error) {
	if cfg.Order < 2 || cfg.Order > 5 {
		// Order 1 is deliberately unsupported (2026-08-04, Paul): never
		// wanted for the product, and the default HRTF set carries no
		// order-1 bank anyway.
		return nil, errors.New("engine: order must be 2..5")
	}
	if cfg.MaxEntities < 1 {
		return nil, errors.New("engine: MaxEntities must be >= 1")
	}
	if cfg.Workers < 1 {
		return nil, errors.New("engine: Workers must be >= 1")
	}

	m := &Mixer{cfg: cfg}
	m.channelCount = common.ChannelCountForOrder(cfg.Order)
	// Size 1.0: the engine is metres-native, so the copied normalized→
	// metres conversion in SetPosition becomes the identity. The single
	// units conversion point (plan I-D) is Pose.position().
	m.encCfg = common.MixerConfig{
		MaxNodes:     cfg.MaxEntities,
		FrameSize:    common.FRAME_SIZE,
		ChannelCount: m.channelCount,
		Order:        cfg.Order,
		Size:         1.0,
		ReverbPreset: cfg.ReverbPreset,
		PureGoMixer:  cfg.PureGoMixer,
	}

	set := hrtf.Default()
	if set.Bank(cfg.Order) == nil {
		// The bilateral binaural decode convolves per-order SH filter
		// banks from the HRTF set; an order the set doesn't carry cannot
		// build sinks. The default set is 3rd-order.
		return nil, fmt.Errorf("engine: HRTF set %q carries no order-%d filter bank (orders %v)",
			set.Manifest.Name, cfg.Order, set.Manifest.Orders)
	}
	// The alignment parameters are process-global and every Mixer uses
	// the same default set, so install exactly once: a second New while
	// another mixer is rendering must not rewrite values that mixer's
	// audio thread is reading.
	installDefaultDelayModel.Do(func() { set.InstallDelayModel() })
	m.convolverSet = binaural.NewConvolverSet(set, m.channelCount)
	m.shared = ambisonic.NewSharedInputs(m.encCfg)

	m.reg = make(map[string]*regEntry)
	m.changes = make(chan func(), 4096)
	m.ents = make(map[string]*entity)
	m.slots = make([]*entity, cfg.MaxEntities)
	m.sourceEntities = make([]*entity, 0, cfg.MaxEntities)
	m.allEnts = make([]*entity, 0, cfg.MaxEntities)
	m.sinkEnts = make([]*entity, 0, cfg.MaxEntities)
	m.channelTable = make(map[string]ChannelParams)
	m.audible = make([][]bool, cfg.MaxEntities)
	m.pairGain = make([][]float32, cfg.MaxEntities)
	m.pairAttExp = make([][]float64, cfg.MaxEntities)
	for i := range m.audible {
		m.audible[i] = make([]bool, cfg.MaxEntities)
		m.pairGain[i] = make([]float32, cfg.MaxEntities)
		m.pairAttExp[i] = make([]float64, cfg.MaxEntities)
	}
	m.personalMutes = make(map[string]map[string]bool)
	m.personalSolos = make(map[string]map[string]bool)
	m.stop = make(chan struct{})

	m.workers = make([]*worker, cfg.Workers)
	for i := range m.workers {
		m.workers[i] = m.newWorker()
	}
	return m, nil
}

// Close stops Run and the worker pool, then settles all pending
// lifecycle work inline: the changes queue is drained (closure retention
// must not outlive the audio thread — a queued RemoveSink holds cgo
// resources the GC cannot free) and any sinks still attached are
// destroyed. If Run is driving the mixer, Close signals it and WAITS
// for it to return (at most one frame + tick) before touching audio-
// thread state; a host calling Process directly must have ceased its
// calls. Either way the closing goroutine is the audio thread's
// successor.
func (m *Mixer) Close() {
	m.mu.Lock()
	m.closed = true
	runDone := m.runDone
	m.mu.Unlock()
	m.stopOnce.Do(func() {
		close(m.stop)
		if runDone != nil {
			<-runDone
		}
		for _, w := range m.workers {
			close(w.jobs)
		}
		m.drainChanges()
		for _, e := range m.slots {
			if e == nil {
				continue
			}
			if e.sink != nil {
				e.sink.destroy()
				e.sink = nil
			}
			for order, k := range e.ambiSinks {
				k.enc.BeforeDestroy()
				delete(e.ambiSinks, order)
			}
			e.ambiEnc = nil
		}
	})
}

// enqueue hands a mutation to the audio thread. Blocks only if the
// control plane outruns the render loop by the queue depth.
func (m *Mixer) enqueue(f func()) {
	m.changes <- f
}

func (m *Mixer) drainChanges() {
	for {
		select {
		case f := <-m.changes:
			f()
		default:
			return
		}
	}
}

// AddSource attaches a source to entity id, creating the entity if
// needed. The returned Source's Write methods are ready immediately —
// audio written before the next tick simply lands in the jitter buffer.
func (m *Mixer) AddSource(id string, cfg SourceConfig) (*Source, error) {
	cfg.applyDefaults()

	src := &Source{m: m, id: id, initialPose: cfg.InitialPose}
	if cfg.TestTone > 0 {
		src.tone = inout.NewSineMonoInput(cfg.TestTone, common.SAMPLE_RATE, common.FRAME_SIZE)
	} else {
		// The declared floors compose by max (design §8): the quality
		// level's floor and the FEC floor derived from the declared
		// maximum redundancy offset. Both are provisioned before the
		// first packet — never learned from an audible failure.
		floor, robustness, capacity := buffers.QualityLevel(cfg.Quality)
		if f := buffers.FECFloor(cfg.Redundancy, cfg.JitterWriterFrame); f > floor {
			floor = f
		}
		src.jitter = buffers.NewJitterBuffer(buffers.JitterBufferConfig{
			SampleRate:      common.SAMPLE_RATE,
			NumChannels:     1,
			WriterFrameSize: cfg.JitterWriterFrame,
			ReaderFrameSize: cfg.JitterReaderFrame,
			Floor:           floor,
			Robustness:      robustness,
			Capacity:        capacity,
		})
		if cfg.Codec == SourceCodecRawF32 {
			src.raw = true
			src.rawScratch = make([]float32, FrameSize)
		} else {
			src.decoder = inout.NewOpusInputDecoder(cfg.InputChannels)
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	re := m.reg[id]
	if re != nil && re.src != nil {
		m.mu.Unlock()
		return nil, ErrDuplicateSource
	}
	if re == nil {
		if m.entityCount >= m.cfg.MaxEntities {
			m.mu.Unlock()
			return nil, ErrFull
		}
		re = &regEntry{}
		m.reg[id] = re
		m.entityCount++
	}
	re.src = src
	m.mu.Unlock()

	m.enqueue(func() {
		e := m.ensureEntity(id)
		e.src = src
		e.enc.SetPosition(cfg.InitialPose.position())
		e.enc.SetHeadFrameSource(cfg.HeadFrame)
		m.markAudibleDirty()
	})
	return src, nil
}

// AddSink attaches a sink to entity id, creating the entity if needed.
// Frames start flowing to w on the next ticks.
func (m *Mixer) AddSink(id string, w FrameWriter) (*Sink, error) {
	return m.AddSinkCodec(id, w, SinkCodecOpus)
}

// AddSinkCodec is AddSink with the egress codec chosen (SinkCodecRawF32
// for codec-free capacity work; AddSink is the Opus default).
func (m *Mixer) AddSinkCodec(id string, w FrameWriter, codec SinkCodec) (*Sink, error) {
	dec := binaural.NewConvolverDecoder(m.convolverSet)
	k := &Sink{m: m, id: id, w: w}
	if codec == SinkCodecRawF32 {
		k.out = inout.NewConvolverRawF32OutputEncoder(dec, m.channelCount)
	} else {
		k.out = inout.NewConvolverOpusOutputEncoder(dec, m.channelCount)
	}
	if err := m.addSink(id, k); err != nil {
		k.out.BeforeDestroy()
		return nil, err
	}
	return k, nil
}

// addSink is AddSink minus sink construction (shared with the test-only
// null sink, which mirrors the incumbent budget test's decode-then-
// discard listeners).
func (m *Mixer) addSink(id string, k *Sink) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	re := m.reg[id]
	if re != nil && re.sink != nil {
		m.mu.Unlock()
		return ErrDuplicateSink
	}
	if re == nil {
		if m.entityCount >= m.cfg.MaxEntities {
			m.mu.Unlock()
			return ErrFull
		}
		re = &regEntry{}
		m.reg[id] = re
		m.entityCount++
	}
	re.sink = k
	m.mu.Unlock()

	m.enqueue(func() {
		e := m.ensureEntity(id)
		e.sink = k
		m.markAudibleDirty()
	})
	return nil
}

// Entities returns the registered entity ids, unordered — the
// control-plane view: an id is present from the Add* that created it
// until the Remove* that dropped its last half. A diagnostic and
// assertion surface (the host's conn-map ≡ engine-set invariant);
// not for hot paths.
func (m *Mixer) Entities() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.reg))
	for id := range m.reg {
		ids = append(ids, id)
	}
	return ids
}

// RemoveSource detaches entity id's source. Idempotent. The entity
// disappears (and its slot frees) when both halves are gone. The caller
// stops Write calls before removal; a straggler is harmless.
func (m *Mixer) RemoveSource(id string) {
	m.mu.Lock()
	re := m.reg[id]
	if re == nil || re.src == nil {
		m.mu.Unlock()
		return
	}
	re.src = nil
	if re.sink == nil && len(re.ambi) == 0 {
		delete(m.reg, id)
		m.entityCount--
	}
	m.mu.Unlock()

	m.enqueue(func() {
		e := m.ents[id]
		if e == nil {
			return
		}
		e.src = nil
		if e.sink == nil && len(e.ambiSinks) == 0 {
			m.removeEntity(e)
		}
	})
}

// RemoveSink detaches entity id's sink and releases its cgo resources on
// the audio thread — the funnel lesson (encoder frees only on the mixer
// goroutine, after rendering stops) carried as a rule. Idempotent.
func (m *Mixer) RemoveSink(id string) {
	m.mu.Lock()
	re := m.reg[id]
	if re == nil || re.sink == nil {
		m.mu.Unlock()
		return
	}
	re.sink = nil
	if re.src == nil && len(re.ambi) == 0 {
		delete(m.reg, id)
		m.entityCount--
	}
	m.mu.Unlock()

	m.enqueue(func() {
		e := m.ents[id]
		if e == nil || e.sink == nil {
			return
		}
		e.sink.destroy()
		e.sink = nil
		if e.src == nil {
			m.removeEntity(e)
		}
	})
}

// SetRenderParams replaces entity id's render params wholesale (shape
// (C), tier one). The host writes DefaultRenderParams fields back on
// clears; values are applied literally. Channel memberships take effect
// on the next tick via the audibility matrix.
func (m *Mixer) SetRenderParams(id string, p RenderParams) error {
	p.SourceChannels = append([]string(nil), p.SourceChannels...)
	p.SinkChannels = append([]string(nil), p.SinkChannels...)

	m.mu.Lock()
	if m.reg[id] == nil {
		m.mu.Unlock()
		return ErrUnknownEntity
	}
	m.mu.Unlock()

	m.enqueue(func() {
		e := m.ents[id]
		if e == nil {
			return
		}
		e.params = p
		e.enc.SetGain(p.Gain)
		e.enc.SetAttenuation(p.Attenuation)
		e.enc.SetSize(p.Size)
		e.enc.SetDirectivity(p.Directivity)
		m.markAudibleDirty()
	})
	return nil
}

// SetChannel sets the space-scoped params for a named channel (tier
// two). Channels exist by being named; unnamed channels behave as
// DefaultChannelParams. Takes effect on the next tick via the pair
// record (see ChannelParams for the §4 composition semantics).
func (m *Mixer) SetChannel(name string, p ChannelParams) {
	if p.Attenuation != nil {
		v := *p.Attenuation
		p.Attenuation = &v // detach from the caller's pointer
	}
	m.enqueue(func() {
		m.channelTable[name] = p
		m.markAudibleDirty()
	})
}

// SetPersonalMute sets/clears listener's personal mute of other (base
// profile §6; semantics decided 2026-08-04): muting silences the pair
// in BOTH directions. Keyed by entity id — the ids need not be live;
// the veto applies whenever both are. The listener's own entries are
// dropped when it leaves (its preferences are connection-scoped).
func (m *Mixer) SetPersonalMute(listenerID, otherID string, muted bool) {
	m.enqueue(func() {
		setPersonal(m.personalMutes, listenerID, otherID, muted)
		m.markAudibleDirty()
	})
}

// SetPersonalSolo sets/clears other's membership in listener's solo
// set: a non-empty set restricts the listener to exactly that set, and
// wins over personal mutes (space moderation mute always still wins).
func (m *Mixer) SetPersonalSolo(listenerID, otherID string, solo bool) {
	m.enqueue(func() {
		setPersonal(m.personalSolos, listenerID, otherID, solo)
		m.markAudibleDirty()
	})
}

func setPersonal(m map[string]map[string]bool, owner, other string, on bool) {
	if on {
		if m[owner] == nil {
			m[owner] = make(map[string]bool)
		}
		m[owner][other] = true
		return
	}
	delete(m[owner], other)
	if len(m[owner]) == 0 {
		delete(m, owner)
	}
}

// SetEntityMuted is the space-scoped moderation mute: entity id's source
// is silenced in every mix. The entity keeps hearing.
func (m *Mixer) SetEntityMuted(id string, muted bool) error {
	m.mu.Lock()
	if m.reg[id] == nil {
		m.mu.Unlock()
		return ErrUnknownEntity
	}
	m.mu.Unlock()

	m.enqueue(func() {
		if e := m.ents[id]; e != nil {
			e.enc.SetSpaceMuted(muted)
		}
	})
	return nil
}

// ensureEntity creates the audio-side entity on first need: slot,
// identity token, encoder (metres-native, defaults until params arrive).
// Audio thread only.
func (m *Mixer) ensureEntity(id string) *entity {
	if e := m.ents[id]; e != nil {
		return e
	}
	slot := -1
	for i, s := range m.slots {
		if s == nil {
			slot = i
			break
		}
	}
	if slot < 0 {
		// Unreachable: the control-plane capacity check gates admission.
		panic("engine: no free slot")
	}
	e := &entity{id: id, uid: uuid.New(), slot: slot, params: DefaultRenderParams()}
	e.enc = ambisonic.NewEncoder(e.uid, true, e.params.Gain, e.params.Attenuation, m.encCfg, slot)
	e.enc.SetSharedInputs(m.shared)
	// The listener's view of the pair record: its slot's rows, stable
	// for the entity's lifetime (rewritten in place by recomputeAudible).
	e.enc.SetPairScalars(m.pairGain[slot], m.pairAttExp[slot])
	e.peers = make([]*ambisonic.Encoder, 0, m.cfg.MaxEntities)
	m.ents[id] = e
	m.slots[slot] = e
	return e
}

// removeEntity frees the slot. The encoder is plain Go memory; slot
// reuse hygiene (delay ramps, NFC state) is the encoders' own
// slot-owner/generation logic. Audio thread only. The entity's OWN
// personal mute/solo entries die with it (connection-scoped, §6 —
// and required for correctness: the state sweep's marker-first order
// means the host's wiring never relays the swept mute/solo clears);
// entries targeting it in other listeners' sets persist, so a rejoin
// under the same id is still muted by those who muted it.
func (m *Mixer) removeEntity(e *entity) {
	delete(m.ents, e.id)
	delete(m.personalMutes, e.id)
	delete(m.personalSolos, e.id)
	m.slots[e.slot] = nil
	m.markAudibleDirty()
}
