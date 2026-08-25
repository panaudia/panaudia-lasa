package engine

// AmbiSink — the raw ambisonic sink formats (build-plan P4, design
// agreed 2026-08-04/05): a HEAD-CENTRED, WORLD-FRAME render of the
// entity's perspective (classic single-bus encoder — rotation never
// applied per decision 10, no ITD/parallax/NFC, those are binaural-path
// perks), truncated to the format order, rescaled N3D→SN3D, and encoded
// as one Opus multistream packet per 5 ms frame (uncoupled mono
// streams, ACN order — the pinned wire layout).
//
// One classic encoder per entity serves every ambi order subscribed on
// it (truncation of an SH field to a lower order is exact per channel);
// binaural and ambi renders run simultaneously and independently. The
// engine is N3D internally by construction (orthonormal SH constants ×
// SQRT_4_PI), so the boundary conversion is the constant per-order
// divide by √(2l+1).

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/panaudia/panaudia-lasa/engine/ambisonic"

	"github.com/panaudia/panaudia-lasa/engine/inout"
)

// sn3dScale[l] = 1/√(2l+1): the N3D→SN3D per-order factor.
// channelOrder[c] = the SH order of ACN channel c.
var (
	sn3dScale    [6]float32
	channelOrder [36]int
)

func init() {
	for l := 0; l <= 5; l++ {
		sn3dScale[l] = float32(1 / math.Sqrt(float64(2*l+1)))
		for c := l * l; c < (l+1)*(l+1); c++ {
			channelOrder[c] = l
		}
	}
}

// AmbiSink is one (entity, ambi order) render target. SetPose may be
// called from any single goroutine; frames flow on render workers.
type AmbiSink struct {
	m     *Mixer
	id    string
	order int
	w     FrameWriter

	enc          *inout.OpusMSEncoder
	pose         poseSlot
	interleave   []float32 // (order+1)² × FrameSize encode scratch
	encodeErrors atomic.Uint64
}

// SetPose updates the listening pose, latest-wins. Only the position is
// consumed — the rendered field is world-frame by design.
func (k *AmbiSink) SetPose(p Pose) { k.pose.store(p) }

// EncodeErrors counts frames the multistream encoder rejected (each
// dropped; the stream continues). Safe from any goroutine.
func (k *AmbiSink) EncodeErrors() uint64 { return k.encodeErrors.Load() }

// AddAmbiSink attaches a raw ambisonic sink of the given order (2 or 3,
// and at most the bus order) to entity id, creating the entity if
// needed. One sink per (entity, order); binaural and other ambi orders
// may coexist on the same entity.
func (m *Mixer) AddAmbiSink(id string, order int, w FrameWriter) (*AmbiSink, error) {
	if order < 2 || order > 3 {
		return nil, fmt.Errorf("engine: ambi sink order must be 2 or 3, got %d", order)
	}
	if order > m.cfg.Order {
		return nil, fmt.Errorf("engine: ambi sink order %d exceeds the bus order %d", order, m.cfg.Order)
	}
	channels := (order + 1) * (order + 1)
	ms, err := inout.NewOpusMSEncoder(channels)
	if err != nil {
		return nil, err
	}
	k := &AmbiSink{
		m: m, id: id, order: order, w: w,
		enc:        ms,
		interleave: make([]float32, channels*FrameSize),
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		ms.BeforeDestroy()
		return nil, ErrClosed
	}
	re := m.reg[id]
	if re != nil && re.ambi[order] != nil {
		m.mu.Unlock()
		ms.BeforeDestroy()
		return nil, ErrDuplicateSink
	}
	if re == nil {
		if m.entityCount >= m.cfg.MaxEntities {
			m.mu.Unlock()
			ms.BeforeDestroy()
			return nil, ErrFull
		}
		re = &regEntry{}
		m.reg[id] = re
		m.entityCount++
	}
	if re.ambi == nil {
		re.ambi = make(map[int]*AmbiSink, 2)
	}
	re.ambi[order] = k
	m.mu.Unlock()

	m.enqueue(func() {
		e := m.ensureEntity(id)
		if e.ambiEnc == nil {
			// The one classic render shared by every ambi order on this
			// entity. Same uid: the entity's own source stays
			// self-excluded, exactly as in its binaural perspective.
			e.ambiEnc = ambisonic.NewClassicEncoder(e.uid, false,
				e.params.Gain, e.params.Attenuation, m.encCfg, e.slot)
			e.ambiEnc.SetSharedInputs(m.shared)
			e.ambiEnc.SetPairScalars(m.pairGain[e.slot], m.pairAttExp[e.slot])
		}
		if e.ambiSinks == nil {
			e.ambiSinks = make(map[int]*AmbiSink, 2)
		}
		e.ambiSinks[order] = k
		m.markAudibleDirty()
	})
	return k, nil
}

// RemoveAmbiSink detaches entity id's ambi sink of the given order and
// releases its cgo encoder on the audio thread (the funnel lesson
// carried as a rule). Idempotent.
func (m *Mixer) RemoveAmbiSink(id string, order int) {
	m.mu.Lock()
	re := m.reg[id]
	if re == nil || re.ambi[order] == nil {
		m.mu.Unlock()
		return
	}
	delete(re.ambi, order)
	if re.src == nil && re.sink == nil && len(re.ambi) == 0 {
		delete(m.reg, id)
		m.entityCount--
	}
	m.mu.Unlock()

	m.enqueue(func() {
		e := m.ents[id]
		if e == nil {
			return
		}
		if k := e.ambiSinks[order]; k != nil {
			k.enc.BeforeDestroy()
			delete(e.ambiSinks, order)
		}
		if len(e.ambiSinks) == 0 {
			e.ambiEnc = nil // plain Go memory; the ms encoders were the cgo half
		}
		if e.src == nil && e.sink == nil && len(e.ambiSinks) == 0 {
			m.removeEntity(e)
		}
	})
}

// emit runs on a render worker in the Out phase: truncate the classic
// bus to the format order, rescale N3D→SN3D, interleave, multistream-
// encode, write. Allocation-free.
func (k *AmbiSink) emit(planar []float32) {
	channels := (k.order + 1) * (k.order + 1)
	for c := 0; c < channels; c++ {
		s := sn3dScale[channelOrder[c]]
		row := planar[c*FrameSize : (c+1)*FrameSize]
		for i, v := range row {
			k.interleave[i*channels+c] = v * s
		}
	}
	pkt, err := k.enc.Encode(k.interleave)
	if err != nil {
		k.encodeErrors.Add(1)
		return // an encode error drops the frame; the stream continues
	}
	k.w.WriteFrame(pkt, k.m.frameStart)
}
