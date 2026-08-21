package main

// stateWiring is the profile consumer (design F): one state observer,
// base.ParseOp for the vocabulary, engine mutators for the mechanism.
// The engine stays key-blind; the profile stays engine-blind; this file
// is the only place the two vocabularies meet.
//
// The wiring keeps derived state because the two sides have different
// lifetimes: the engine refuses params for entities it doesn't hold and
// forgets them at departure, while state ops arrive before EntityJoined
// (the admission group) and space-scoped keys (entity mutes, the channel
// table) outlive any entity. Every mutator call is therefore "apply if
// live, and re-apply at join from the derived state".
//
// The observer runs on the state engine's actor: everything here is map
// updates under one mutex plus mixer enqueues — nothing calls back into
// the state engine.

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/panaudia/lasa/profile/base"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

type stateWiring struct {
	mixer *engine.Mixer
	b     *backend

	mu sync.Mutex
	// ents holds a derived entry per live existence marker (§6: entity
	// keys under an absent marker are ignored; the marker-first sweep
	// makes departure drop the entry before its subtree clears arrive).
	ents map[string]*derivedEntity
	// muted is the space-scoped moderation mute set — it survives entity
	// departure in state, so it must survive it here too (a muted entity
	// that rejoins is re-muted at the join flush).
	muted map[string]bool
	// channels is the space-scoped channel table, composed from the three
	// per-channel keys into the engine's one ChannelParams.
	channels map[string]engine.ChannelParams

	// invalid counts recognised keys with out-of-range values (the
	// profile's drop-and-count posture); a stats surface for S6.
	invalid atomic.Uint64
}

// derivedEntity mirrors the entity's consumer-vocabulary state. Scalars
// default per the profile; memberships are literal — state carries the
// materialized effective sets (base.AdmissionGroup resolves the default
// at the gate), so this never re-applies "main". Admission guarantees at
// least one channel; a set emptied later is a producer's moderation
// clear, and correctly renders that entity inaudible.
type derivedEntity struct {
	gain, attenuation, size, directivity float64
	heardIn, hears                       map[string]bool
}

func newDerivedEntity() *derivedEntity {
	return &derivedEntity{
		gain:        base.DefaultGain,
		attenuation: base.DefaultAttenuation,
		size:        base.DefaultSize,
		directivity: base.DefaultDirectivity,
		heardIn:     map[string]bool{},
		hears:       map[string]bool{},
	}
}

func (d *derivedEntity) renderParams() engine.RenderParams {
	return engine.RenderParams{
		Gain:           d.gain,
		Attenuation:    d.attenuation,
		Size:           d.size,
		Directivity:    d.directivity,
		SourceChannels: sortedKeys(d.heardIn),
		SinkChannels:   sortedKeys(d.hears),
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func newStateWiring(mixer *engine.Mixer, b *backend) *stateWiring {
	return &stateWiring{
		mixer:    mixer,
		b:        b,
		ents:     map[string]*derivedEntity{},
		muted:    map[string]bool{},
		channels: map[string]engine.ChannelParams{},
	}
}

// prefixes are the observer subscription (plain §6.3 prefixes).
func (w *stateWiring) prefixes() []string { return []string{"lasa.entity.", "lasa.space."} }

// observe is the Observer callback: one applied op in, at most one
// mutator call out. Mutator refusals (ErrUnknownEntity) are benign by
// design — the entity is either pre-join (the entityJoined flush will
// apply the derived state) or already departing (the sweep is clearing
// these very keys).
func (w *stateWiring) observe(key string, value []byte, cleared bool) {
	if id, ok := markerEntity(key); ok {
		w.mu.Lock()
		if cleared {
			delete(w.ents, id)
		} else if w.ents[id] == nil {
			w.ents[id] = newDerivedEntity()
		}
		w.mu.Unlock()
		return
	}
	ev, err := base.ParseOp(key, value, cleared)
	if err != nil {
		w.invalid.Add(1)
		return
	}
	switch e := ev.(type) {
	case base.EntityRender:
		w.entityUpdate(e.EntityID, func(d *derivedEntity) {
			switch e.Field {
			case base.FieldGain:
				d.gain = e.Value
			case base.FieldAttenuation:
				d.attenuation = e.Value
			case base.FieldSize:
				d.size = e.Value
			case base.FieldDirectivity:
				d.directivity = e.Value
			}
		})
	case base.ChannelMembership:
		w.entityUpdate(e.EntityID, func(d *derivedEntity) {
			set := d.heardIn
			if e.Hears {
				set = d.hears
			}
			if e.Member {
				set[e.Channel] = true
			} else {
				delete(set, e.Channel)
			}
		})
	case base.EntityMuted:
		w.mu.Lock()
		if e.Muted {
			w.muted[e.EntityID] = true
		} else {
			delete(w.muted, e.EntityID)
		}
		w.mu.Unlock()
		_ = w.mixer.SetEntityMuted(e.EntityID, e.Muted)
	case base.ChannelMuted:
		w.channelUpdate(e.Channel, func(p *engine.ChannelParams) { p.Muted = e.Muted })
	case base.ChannelGain:
		w.channelUpdate(e.Channel, func(p *engine.ChannelParams) { p.Gain = e.Gain })
	case base.ChannelAttenuation:
		w.channelUpdate(e.Channel, func(p *engine.ChannelParams) {
			p.Attenuation = e.Attenuation // nil = no override, end to end
		})
	case base.PersonalMute:
		// Id-keyed and unguarded by the marker: the engine's pair record
		// applies the veto whenever both ids are live and sweeps the
		// owner's entries at its departure (the marker-first sweep means
		// these clears never arrive here).
		w.mixer.SetPersonalMute(e.OwnerID, e.OtherID, e.On)
	case base.PersonalSolo:
		w.mixer.SetPersonalSolo(e.OwnerID, e.OtherID, e.On)
	case base.Dof:
		w.b.setDof(e.EntityID, e.Value)
		// Frame is config-only in v0 (head rejected at admission).
		// ClientBlocked / RoleBlocked are the moderation gate's own
		// observer's business.
	}
}

// markerEntity reports whether key is a bare existence marker
// lasa.entity.{id} (no further dot — identifiers cannot contain dots).
func markerEntity(key string) (string, bool) {
	const p = "lasa.entity."
	if !strings.HasPrefix(key, p) {
		return "", false
	}
	id := key[len(p):]
	if id == "" || strings.Contains(id, ".") {
		return "", false
	}
	return id, true
}

func (w *stateWiring) entityUpdate(id string, fn func(*derivedEntity)) {
	w.mu.Lock()
	d := w.ents[id]
	if d == nil { // marker absent: ignore the key (§6)
		w.mu.Unlock()
		return
	}
	fn(d)
	p := d.renderParams()
	w.mu.Unlock()
	_ = w.mixer.SetRenderParams(id, p)
}

func (w *stateWiring) channelUpdate(name string, fn func(*engine.ChannelParams)) {
	w.mu.Lock()
	p, ok := w.channels[name]
	if !ok {
		p = engine.DefaultChannelParams()
	}
	fn(&p)
	w.channels[name] = p
	w.mu.Unlock()
	w.mixer.SetChannel(name, p)
}

// entityJoined re-applies the derived state once the engine holds the
// entity — the funnel calls it from EntityJoined, after AddSource. The
// admission group has already been applied (start() writes it before
// EntityJoined), so the derived entry carries the config's values.
func (w *stateWiring) entityJoined(id string) {
	w.mu.Lock()
	d := w.ents[id]
	var p engine.RenderParams
	if d != nil {
		p = d.renderParams()
	}
	muted := w.muted[id]
	w.mu.Unlock()
	if d != nil {
		_ = w.mixer.SetRenderParams(id, p)
	}
	if muted {
		_ = w.mixer.SetEntityMuted(id, true)
	}
}
