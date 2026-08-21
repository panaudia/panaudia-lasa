package engine

import "testing"

func listenerEnergy(m *Mixer, id string) float64 {
	e := m.ents[id]
	var energy float64
	for _, v := range e.enc.Output {
		energy += float64(v) * float64(v)
	}
	return energy
}

func settle(m *Mixer, ticks int) {
	for i := 0; i < ticks; i++ {
		m.Process()
	}
}

func withChannels(source, sink []string) RenderParams {
	p := DefaultRenderParams()
	p.SourceChannels = source
	p.SinkChannels = sink
	return p
}

func TestChannelRouting(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// A stage source and two listeners: front is in the stage channel,
	// lobby is not.
	if _, err := m.AddSource("performer", SourceConfig{TestTone: 300, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("front", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("lobby", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRenderParams("performer", withChannels([]string{"stage"}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRenderParams("front", withChannels(nil, []string{"stage", "main"})); err != nil {
		t.Fatal(err)
	}
	// lobby keeps default ["main"] sinks.
	settle(m, 5)

	if listenerEnergy(m, "front") == 0 {
		t.Fatal("front (shares stage) hears nothing")
	}
	if e := listenerEnergy(m, "lobby"); e != 0 {
		t.Fatalf("lobby (no shared channel) hears the performer: %v", e)
	}

	// Muting the stage channel silences front too.
	m.SetChannel("stage", ChannelParams{Muted: true, Gain: 1})
	settle(m, 5)
	if e := listenerEnergy(m, "front"); e != 0 {
		t.Fatalf("muted channel still audible: %v", e)
	}

	// Unmute: audible again.
	m.SetChannel("stage", DefaultChannelParams())
	settle(m, 5)
	if listenerEnergy(m, "front") == 0 {
		t.Fatal("unmuted channel inaudible")
	}

	// Membership change at runtime: performer moves to main — lobby
	// hears, front still does (front also sinks main).
	if err := m.SetRenderParams("performer", withChannels([]string{"main"}, nil)); err != nil {
		t.Fatal(err)
	}
	settle(m, 5)
	if listenerEnergy(m, "lobby") == 0 {
		t.Fatal("lobby deaf after performer joined main")
	}
	if listenerEnergy(m, "front") == 0 {
		t.Fatal("front deaf after performer joined main")
	}
}

func TestExplicitEmptyChannelsMeansNone(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if _, err := m.AddSource("mute-by-config", SourceConfig{TestTone: 300, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("listener", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	// Empty source-channel list: nothing to share, so audible to nobody.
	// This is the general intersection rule, exercised at the engine API
	// — LASA config cannot reach this state (base.effectiveChannels
	// defaults an empty list at admission), but a moderation clear can.
	if err := m.SetRenderParams("mute-by-config", withChannels([]string{}, nil)); err != nil {
		t.Fatal(err)
	}
	settle(m, 5)
	if e := listenerEnergy(m, "listener"); e != 0 {
		t.Fatalf("empty-channel source audible: %v", e)
	}
}
