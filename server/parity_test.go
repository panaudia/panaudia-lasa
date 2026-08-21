package main

import (
	"reflect"
	"testing"

	"github.com/panaudia/lasa/connect"
	"github.com/panaudia/lasa/profile/base"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// TestDefaultsParity holds the two libraries' documented defaults equal
// (design F): the profile's absence-means-default values and the
// engine's own defaults never import each other — this composition test
// is the only place both are visible, so it is where drift is caught.
func TestDefaultsParity(t *testing.T) {
	p := engine.DefaultRenderParams()
	if p.Gain != base.DefaultGain {
		t.Errorf("gain: engine %v, profile %v", p.Gain, base.DefaultGain)
	}
	if p.Attenuation != base.DefaultAttenuation {
		t.Errorf("attenuation: engine %v, profile %v", p.Attenuation, base.DefaultAttenuation)
	}
	if p.Size != base.DefaultSize {
		t.Errorf("size: engine %v, profile %v", p.Size, base.DefaultSize)
	}
	if p.Directivity != base.DefaultDirectivity {
		t.Errorf("directivity: engine %v, profile %v", p.Directivity, base.DefaultDirectivity)
	}
	if !reflect.DeepEqual(p.SourceChannels, base.DefaultChannels()) {
		t.Errorf("source channels: engine %v, profile %v", p.SourceChannels, base.DefaultChannels())
	}
	if !reflect.DeepEqual(p.SinkChannels, base.DefaultChannels()) {
		t.Errorf("sink channels: engine %v, profile %v", p.SinkChannels, base.DefaultChannels())
	}

	cp := engine.DefaultChannelParams()
	if cp.Muted {
		t.Error("default channel must not be muted")
	}
	if cp.Gain != base.DefaultChannelGain {
		t.Errorf("channel gain: engine %v, profile %v", cp.Gain, base.DefaultChannelGain)
	}
	if cp.Attenuation != nil {
		t.Errorf("channel attenuation: engine default %v, profile says unset = no override", *cp.Attenuation)
	}

	// The funnel's config-side dof default matches the profile's.
	if got := entityDof(&connect.ResolvedEntity{}); got != base.DefaultDof {
		t.Errorf("dof: funnel %v, profile %v", got, base.DefaultDof)
	}

	// The wiring's fresh derived entry equals the engine's defaults for
	// the scalar fields — the membership sets start empty by design
	// (state carries materialized memberships; see derivedEntity).
	d := newDerivedEntity().renderParams()
	d.SourceChannels, d.SinkChannels = p.SourceChannels, p.SinkChannels
	if !reflect.DeepEqual(d, p) {
		t.Errorf("derived defaults %+v, engine defaults %+v", d, p)
	}
}
