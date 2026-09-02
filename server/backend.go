package main

// backend is the composition adapter: the lasa Backend SPI on one side,
// engine calls on the other. It is the lifecycle funnel of design §5 —
// the only code that mutates the conn-map and the only code that calls
// the engine's Add*/Remove*. Departure causes are exclusively
// transport-plane (the shell's EntityLeft); engine errors are fatal to
// the connection, never absorbed; the engine's own registry is a
// backstop assertion, not policy.
//
// The audio path (S3): each entity's ingress is Depacketizer-wrapped —
// raw datagrams in, stream-ordered Frame/Lost out — and fans out from
// the receive goroutine with no lock, alloc, or block: pose to the
// source ring (with the audio), the sink slot, and the presence slot;
// audio to WriteOpus; provable loss to Conceal. Sinks attach on the
// shell's first-subscriber signal via the FrameWriter→SinkWriter
// bridge, on the engine's global sample clock.

import (
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panaudia/lasa/connect"
	"github.com/panaudia/lasa/ident"
	"github.com/panaudia/lasa/presence"
	"github.com/panaudia/lasa/server"
	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

type backend struct {
	mixer  *engine.Mixer
	srv    *server.Server // set by attach before serving starts
	wiring *stateWiring   // set at composition, before serving starts

	// Codec hatch: the zero values are production (Opus both ways).
	// The capacity ceiling harness sets both to RawF32 to run the
	// composed pipeline with no codec in it (ceiling_test.go).
	sourceCodec engine.SourceCodec
	sinkCodec   engine.SinkCodec

	mu       sync.Mutex
	entities map[string]*entityRec // the conn-map
}

// entityRec is one live entity: its owning client, its engine halves,
// and its presence slot. It is also the entity's Depacketizer
// Receiver — the stream-ordered ingress consumer.
type entityRec struct {
	clientID string
	joined   time.Time // for the departure log line's duration
	src      *engine.Source
	slot     *presence.Slot

	// dof is the entity's enforced degrees of freedom (profile §6:
	// 6 free, 3 turns in place, 0 fixed), applied at ingest. Atomic:
	// written by config now (and the state consumer in S4), read on the
	// receive path.
	dof  atomic.Int32
	home wire.Pose // the pose dof enforcement pins to

	// headFrame marks an aural-HUD entity (profile §6 frame:"head"):
	// its poses are head-relative offsets and it is SOURCE-ONLY — its
	// own sink tracks are refused (no world position to hear from).
	headFrame bool

	// sinks are the entity's live render halves (binaural and/or ambi
	// orders), attached by StartSink and detached on session Stop.
	// Copy-on-write: the ingress goroutine fans poses into every one
	// with a single atomic load (no lock, no alloc).
	sinks atomic.Pointer[[]poseSink]

	dep *server.Depacketizer // stats surface (S6)
}

// poseSink is what both engine sink kinds offer the backend: the pose
// fan-out target, and the encode-error counter the stats surface sums.
type poseSink interface {
	SetPose(engine.Pose)
	EncodeErrors() uint64
}

// addPoseSink / removePoseSink maintain the COW fan-out list (control
// plane; b.mu serialises the writers).
func (b *backend) addPoseSink(rec *entityRec, s poseSink) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Seed the sink with the entity's latest pose (initial pose until
	// the first pose packet): a listener that never moves must still
	// render with its declared attitude. Seeding happens BEFORE the
	// sink joins the fan-out list, so Frame() cannot write concurrently
	// — the pose slot's single-writer rule holds in both phases.
	s.SetPose(enginePose(rec.slot.Pose()))
	old := rec.sinks.Load()
	var next []poseSink
	if old != nil {
		next = append(next, *old...)
	}
	next = append(next, s)
	rec.sinks.Store(&next)
}

func (b *backend) removePoseSink(rec *entityRec, s poseSink) {
	b.mu.Lock()
	defer b.mu.Unlock()
	old := rec.sinks.Load()
	if old == nil {
		return
	}
	next := make([]poseSink, 0, len(*old))
	for _, v := range *old {
		if v != s {
			next = append(next, v)
		}
	}
	rec.sinks.Store(&next)
}

func newBackend(mixer *engine.Mixer) *backend {
	return &backend{
		mixer:    mixer,
		entities: map[string]*entityRec{},
	}
}

func (b *backend) attach(srv *server.Server) { b.srv = srv }

// Admission is not this backend's business (2026-08-13,
// id-capabilities.md): collisions, capacity and the admission writes
// adjudicate atomically at the state engine, and the shell orders the
// superseded predecessor's teardown before EntityJoined. The conn-map
// below is pure mixer bookkeeping.

// EntityJoined adds the entity to the engine and the conn-map, and
// registers it with the presence collector. An engine refusal
// (duplicate from an admission race, capacity) is returned as-is: the
// shell tears the connection down (design §5 rule 2). The returned
// handler is the entity's Depacketizer, wrapping the record itself.
func (b *backend) EntityJoined(clientID string, e connect.ResolvedEntity) (server.EntityHandler, error) {
	home := initialPose(&e)
	headFrame := e.Frame == "head"
	cfg := engine.SourceConfig{
		InitialPose: enginePose(home),
		HeadFrame:   headFrame,
		// LASA mono-object packets carry 5 ms frames (§5.1) — the
		// jitter geometry's W (design: build plan S3).
		JitterWriterFrame: 5 * time.Millisecond,
		// The entity's declared stream intent (lasa-core.md §4.2):
		// quality level 0–2 and intended maximum redundancy offset.
		// The v4 ingest buffer provisions its latency floor from
		// these at admission — before the first packet. The v3-era
		// per-route hand-tuned window bounds (browser-sender profile,
		// 2026-08-05/06) are retired: the v4 buffer measures burst
		// widths itself, and those environments are its acceptance
		// criteria A3–A5 (plan/jitter-v4-design.md §14).
		Quality:    e.Quality,
		Redundancy: e.Redundancy,
		Codec:      b.sourceCodec,
	}
	src, err := b.mixer.AddSource(e.ID, cfg)
	if err != nil {
		return nil, err
	}
	rec := &entityRec{
		clientID:  clientID,
		joined:    time.Now(),
		src:       src,
		home:      home,
		headFrame: headFrame,
	}
	rec.dof.Store(int32(entityDof(&e)))
	rec.slot = b.srv.Presence().Register(e.ID, headFrame, sourceLoudness(src))
	rec.slot.UpdatePose(home)
	rec.dep = server.NewDepacketizer(rec)
	b.mu.Lock()
	b.entities[e.ID] = rec
	n := len(b.entities)
	b.mu.Unlock()
	// The engine now holds the entity: apply the derived profile state
	// (the admission group's writes landed before this call, and any
	// space-scoped mute survives from before the entity existed).
	b.wiring.entityJoined(e.ID)
	slog.Info("entity joined", "entity", e.ID, "name", e.Name, "client", clientID,
		"signed", e.Signed, "frame", e.Frame, "dof", rec.dof.Load(),
		"quality", e.Quality, "redundancy", e.Redundancy, "entities", n)
	return rec.dep, nil
}

// setDof applies a live dof change (profile §6, moderator-writable) to
// the entity's ingest enforcement. Unknown entities are a no-op: the
// entity is pre-join (EntityJoined stores the config value) or gone.
func (b *backend) setDof(entityID string, dof int) {
	b.mu.Lock()
	rec := b.entities[entityID]
	b.mu.Unlock()
	if rec != nil {
		rec.dof.Store(int32(dof))
	}
}

// EntityLeft removes the entity from the conn-map and the engine. It
// can fire for an entity that never joined (a handshake that died
// before EntityJoined) or for a departing predecessor after its id was
// re-admitted under another client — the record's clientID guard makes
// both no-ops. The shell's supersession wait orders these removals
// before a successor's EntityJoined; the mixer's FIFO changes queue
// orders them before a replacement's AddSource.
func (b *backend) EntityLeft(clientID, entityID string) {
	b.mu.Lock()
	rec := b.entities[entityID]
	if rec == nil || rec.clientID != clientID {
		b.mu.Unlock()
		return
	}
	delete(b.entities, entityID)
	n := len(b.entities)
	b.mu.Unlock()
	// RemoveSink covers a sink whose render the shell hasn't stopped
	// (its subscribers outlive the entity); the later session Stop is
	// idempotent.
	b.mixer.RemoveSource(entityID)
	b.mixer.RemoveSink(entityID)
	b.mixer.RemoveAmbiSink(entityID, 2)
	b.mixer.RemoveAmbiSink(entityID, 3)
	// The entity's ingest story goes out with it: what arrived, what
	// was lost, what would not decode.
	slog.Info("entity left", "entity", entityID, "client", clientID,
		"duration", time.Since(rec.joined).Round(time.Second).String(),
		"decodeErrors", rec.src.DecodeErrors(), "encodeErrors", rec.encodeErrors(),
		"depacketizer", rec.dep.Stats(), "entities", n)
}

// Frame consumes one stream-ordered ingress packet (the Depacketizer's
// output; pose and audio each optional). Dof enforcement happens here,
// at ingest (profile §6): dof 3 pins position to the entity's home
// pose, dof 0 discards the pose entirely. The enforced pose fans out
// to the source's pose ring (sample-aligned with the audio), the sink's
// latest-wins slot, and the presence slot — one parse, three readers,
// no clock, no alloc.
func (rec *entityRec) Frame(seq uint64, pose *wire.Pose, audio []byte) {
	var ep *engine.Pose
	if pose != nil {
		if dof := rec.dof.Load(); dof != 0 {
			wp := *pose
			if dof == 3 {
				wp.X, wp.Y, wp.Z = rec.home.X, rec.home.Y, rec.home.Z
			}
			p := enginePose(wp)
			ep = &p
			rec.slot.UpdatePose(wp)
			if l := rec.sinks.Load(); l != nil {
				for _, k := range *l {
					k.SetPose(p)
				}
			}
		}
	}
	rec.src.WriteOpus(seq, ep, audio)
}

// Lost consumes a provable-loss declaration: conceal one frame's worth
// of samples so decoder state and the pose ring's sample accounting
// stay aligned with the sender's timeline. Likely pose-only losses are
// skipped — concealing those would inject samples the sender never
// produced.
func (rec *entityRec) Lost(seq uint64, audioLikely bool) {
	if audioLikely {
		rec.src.Conceal(seq, wire.FrameSamples)
	}
}

// StartSink attaches one of the entity's render halves on the shell's
// first-subscriber signal: the bilateral binaural render, or a raw
// ambisonic render at the format's order (P4 — head-centred,
// world-frame, SN3D multistream; the shell has already checked the
// format is offered — formats carry no role gate, lasa-core.md §3).
func (b *backend) StartSink(entityID, format string, w server.SinkWriter) (server.SinkSession, error) {
	b.mu.Lock()
	rec := b.entities[entityID]
	b.mu.Unlock()
	if rec == nil {
		slog.Warn("sink refused: no live entity", "entity", entityID, "format", format)
		return nil, fmt.Errorf("panaudia-server: no live entity %q", entityID)
	}
	if rec.headFrame {
		// Source-only (profile §6, pinned 2026-08-05): a head-frame
		// entity has no world position to hear from.
		slog.Warn("sink refused: head-frame entity is source-only", "entity", entityID, "format", format)
		return nil, fmt.Errorf("panaudia-server: head-frame entity %q is source-only", entityID)
	}
	var (
		sink poseSink
		stop func()
		err  error
	)
	switch format {
	case ident.TrackBinaural:
		sink, err = b.mixer.AddSinkCodec(entityID, &sinkBridge{w: w}, b.sinkCodec)
		stop = func() { b.mixer.RemoveSink(entityID) }
	case ident.TrackAmbi2, ident.TrackAmbi3:
		order := 2
		if format == ident.TrackAmbi3 {
			order = 3
		}
		sink, err = b.mixer.AddAmbiSink(entityID, order, &sinkBridge{w: w})
		stop = func() { b.mixer.RemoveAmbiSink(entityID, order) }
	default:
		slog.Warn("sink refused: format not supported", "entity", entityID, "format", format)
		return nil, fmt.Errorf("panaudia-server: sink format %q not supported", format)
	}
	if err != nil {
		slog.Warn("sink refused by the engine", "entity", entityID, "format", format, "err", err)
		return nil, err
	}
	b.addPoseSink(rec, sink)
	slog.Info("sink started", "entity", entityID, "client", rec.clientID, "format", format)
	return &sinkSession{b: b, rec: rec, target: sink, stop: stop, entity: entityID, format: format, started: time.Now()}, nil
}

// sinkBridge adapts the engine's FrameWriter to the shell's SinkWriter:
// same frame bytes, same global sample timestamp, on the render worker
// goroutine. A send error is the shell's concern (it drops failed
// subscribers itself); the render must not stop for it.
type sinkBridge struct{ w server.SinkWriter }

func (sb *sinkBridge) WriteFrame(opus []byte, sampleTS uint64) {
	_ = sb.w.WriteFrame(opus, sampleTS)
}

// sinkSession is one live render; the shell Stops it when the last
// subscriber leaves. Stop is idempotent against the entity having
// already departed (EntityLeft's engine removals).
type sinkSession struct {
	b       *backend
	rec     *entityRec
	target  poseSink
	stop    func()
	entity  string
	format  string
	started time.Time
}

func (ss *sinkSession) Stop() {
	ss.b.removePoseSink(ss.rec, ss.target)
	ss.stop()
	slog.Info("sink stopped", "entity", ss.entity, "client", ss.rec.clientID, "format", ss.format,
		"duration", time.Since(ss.started).Round(time.Second).String(), "encodeErrors", ss.target.EncodeErrors())
}

// entitySnapshot is the conn-map's id set — the test-time half of the
// conn-map ≡ engine-set invariant.
func (b *backend) entitySnapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.entities))
	for id := range b.entities {
		ids = append(ids, id)
	}
	return ids
}

// entityDof reads the entity's configured degrees of freedom (absent =
// the profile default 6). Values were schema-validated at resolve.
func entityDof(e *connect.ResolvedEntity) int {
	if e.Dof != nil {
		return *e.Dof
	}
	return 6
}

// initialPose maps an admitted entity's optional initial-pose spec to
// the wire pose dof enforcement pins to (zero pose when absent).
func initialPose(e *connect.ResolvedEntity) wire.Pose {
	var p wire.Pose
	if ip := e.InitialPose; ip != nil {
		if ip.Position != nil {
			p.X, p.Y, p.Z = float32(ip.Position.X), float32(ip.Position.Y), float32(ip.Position.Z)
		}
		if ip.Attitude != nil {
			p.Yaw, p.Pitch, p.Roll = float32(ip.Attitude.Yaw), float32(ip.Attitude.Pitch), float32(ip.Attitude.Roll)
		}
	}
	return p
}

// enginePose widens a wire pose to the engine's metres/radians pose.
func enginePose(p wire.Pose) engine.Pose {
	return engine.Pose{
		X: float64(p.X), Y: float64(p.Y), Z: float64(p.Z),
		Yaw: float64(p.Yaw), Pitch: float64(p.Pitch), Roll: float64(p.Roll),
	}
}

// sourceLoudness adapts the engine's linear post-gain RMS to the wire's
// quantized dBFS presence byte. Zero RMS maps to LoudnessSilent.
func sourceLoudness(src *engine.Source) func() wire.Loudness {
	return func() wire.Loudness {
		return wire.LoudnessFromDBFS(20 * math.Log10(float64(src.Loudness())))
	}
}
