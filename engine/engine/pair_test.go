package engine

// The pair record (Phase C completion, 2026-08-04): per-(source,
// listener) channel gain/attenuation composition per base profile §4,
// and personal mute/solo per §6 with the decided semantics —
// bidirectional mute, solo wins over personal mutes, space moderation
// mute above everything.

import (
	"math"
	"testing"
)

func attp(v float64) *float64 { return &v }

// pairCell reads one resolved cell of the record (white-box; the
// matrices are audio-thread state, settled via Process).
func pairCell(m *Mixer, sourceID, listenerID string) (bool, float32, float64) {
	s, k := m.ents[sourceID], m.ents[listenerID]
	return m.audible[k.slot][s.slot], m.pairGain[k.slot][s.slot], m.pairAttExp[k.slot][s.slot]
}

func addPair(t *testing.T, m *Mixer) {
	t.Helper()
	if _, err := m.AddSource("s", SourceConfig{TestTone: 300, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("k", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
}

func TestPairScalarResolution(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	addPair(t, m)
	settle(m, 1) // drain the adds: entities exist on the audio thread
	base := m.ents["s"].enc.GainFactor()
	baseExp := m.ents["s"].enc.PeerAttenuationExponent()

	check := func(desc string, wantGainMult float32, wantExp float64) {
		t.Helper()
		settle(m, 2)
		aud, g, a := pairCell(m, "s", "k")
		if !aud {
			t.Fatalf("%s: pair not audible", desc)
		}
		if want := base * wantGainMult; math.Abs(float64(g-want)) > 1e-6 {
			t.Errorf("%s: pair gain = %v, want %v", desc, g, want)
		}
		if math.Abs(a-wantExp) > 1e-9 {
			t.Errorf("%s: pair att exponent = %v, want %v", desc, a, wantExp)
		}
	}

	// Defaults: both on main, unnamed channel — identity gain, source's
	// own exponent.
	check("defaults", 1.0, baseExp)

	// One shared channel with a set gain.
	m.SetChannel("main", ChannelParams{Gain: 2.0})
	check("channel gain 2.0", 2.0, baseExp)

	// Two shared channels, mixed set/unset gains: the unset channel
	// competes with the identity — most-audible-wins keeps the pair at
	// max(0.5, 1.0) = 1.0 (§4).
	if err := m.SetRenderParams("s", withChannels([]string{"main", "aux"}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRenderParams("k", withChannels(nil, []string{"main", "aux"})); err != nil {
		t.Fatal(err)
	}
	m.SetChannel("main", ChannelParams{Gain: 0.5})
	check("set 0.5 vs unset identity", 1.0, baseExp)

	// Both set below identity: the higher set value wins (no floor at 1).
	m.SetChannel("aux", ChannelParams{Gain: 0.25})
	check("both set, highest wins", 0.5, baseExp)

	// Attenuation: lowest SET replaces the source's exponent (amplitude
	// exponent = attenuation/2); unset channels contribute no candidate.
	m.SetChannel("main", ChannelParams{Gain: 0.5, Attenuation: attp(3.0)})
	check("one set attenuation", 0.5, 1.5)
	m.SetChannel("aux", ChannelParams{Gain: 0.25, Attenuation: attp(0.5)})
	check("lowest set attenuation wins", 0.5, 0.25)

	// A muted channel contributes nothing to resolution (§4): main muted
	// leaves only aux's 0.25 gain and 0.5 attenuation.
	m.SetChannel("main", ChannelParams{Muted: true, Gain: 0.5, Attenuation: attp(3.0)})
	check("muted channel out of resolution", 0.25, 0.25)

	// Entity gain composes multiplicatively with the channel winner.
	p := DefaultRenderParams()
	p.Gain = 2.0
	p.SourceChannels, p.SinkChannels = []string{"main", "aux"}, []string{"main"}
	if err := m.SetRenderParams("s", p); err != nil {
		t.Fatal(err)
	}
	settle(m, 2)
	_, g, _ := pairCell(m, "s", "k")
	// New GainFactor reflects entity gain 2.0; channel winner 0.25 (aux;
	// main muted). Compare against the recomputed entity factor.
	if want := m.ents["s"].enc.GainFactor() * 0.25; math.Abs(float64(g-want)) > 1e-6 {
		t.Errorf("entity gain composition: pair gain = %v, want %v", g, want)
	}
}

func TestPersonalMuteBidirectional(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// Two speaking listeners: a mutes b — BOTH directions fall silent
	// (decided 2026-08-04: the user expectation, the incumbent's rule).
	for _, id := range []string{"a", "b"} {
		if _, err := m.AddSource(id, SourceConfig{TestTone: 300, InitialPose: Pose{X: 2}}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.AddSink(id, &collectingWriter{}); err != nil {
			t.Fatal(err)
		}
	}
	settle(m, 2)
	if aud, _, _ := pairCell(m, "a", "b"); !aud {
		t.Fatal("baseline: b must hear a")
	}

	m.SetPersonalMute("a", "b", true)
	settle(m, 2)
	if aud, _, _ := pairCell(m, "b", "a"); aud {
		t.Error("a muted b, but a still hears b")
	}
	if aud, _, _ := pairCell(m, "a", "b"); aud {
		t.Error("a muted b, but b still hears a (mute must be bidirectional)")
	}

	m.SetPersonalMute("a", "b", false)
	settle(m, 2)
	if aud, _, _ := pairCell(m, "a", "b"); !aud {
		t.Error("mute cleared, still silent")
	}

	// The muter's entries die with it: a leaves and returns, b audible.
	m.SetPersonalMute("a", "b", true)
	m.RemoveSource("a")
	m.RemoveSink("a")
	if _, err := m.AddSource("a", SourceConfig{TestTone: 300, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("a", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	settle(m, 2)
	if aud, _, _ := pairCell(m, "b", "a"); !aud {
		t.Error("rejoined muter still carries its old mute (must have been swept)")
	}
}

func TestPersonalSoloWinsOverMutes(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, id := range []string{"x", "y"} {
		if _, err := m.AddSource(id, SourceConfig{TestTone: 300, InitialPose: Pose{X: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.AddSink("k", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	settle(m, 2)

	// Solo restricts the listener to the solo'd set.
	m.SetPersonalSolo("k", "x", true)
	settle(m, 2)
	if aud, _, _ := pairCell(m, "x", "k"); !aud {
		t.Error("solo'd source inaudible")
	}
	if aud, _, _ := pairCell(m, "y", "k"); aud {
		t.Error("non-solo'd source audible while a solo is active")
	}

	// Solo wins over personal mutes — even the source's own mute of the
	// listener (the incumbent rule, kept).
	m.SetPersonalMute("k", "x", true)
	m.SetPersonalMute("x", "k", true)
	settle(m, 2)
	if aud, _, _ := pairCell(m, "x", "k"); !aud {
		t.Error("solo must win over personal mutes")
	}

	// Clearing the solo re-exposes y and lets the mutes bite on x.
	m.SetPersonalSolo("k", "x", false)
	settle(m, 2)
	if aud, _, _ := pairCell(m, "x", "k"); aud {
		t.Error("mutes must apply once the solo clears")
	}
	if aud, _, _ := pairCell(m, "y", "k"); !aud {
		t.Error("y must be audible once the solo clears")
	}
}

// TestSpaceMuteBeatsSolo: the moderation mute silences the source in
// every render — soloing it must NOT bypass moderation (fixed relative
// to the incumbent, whose solo check preceded the space-mute veto; our
// Layer-2 solo never reaches the encoder's legacy sets).
func TestSpaceMuteBeatsSolo(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	addPair(t, m)
	m.SetPersonalSolo("k", "s", true)
	if err := m.SetEntityMuted("s", true); err != nil {
		t.Fatal(err)
	}
	settle(m, 5)
	if e := listenerEnergy(m, "k"); e != 0 {
		t.Fatalf("space-muted source audible through a solo: %v", e)
	}
	if err := m.SetEntityMuted("s", false); err != nil {
		t.Fatal(err)
	}
	settle(m, 5)
	if listenerEnergy(m, "k") == 0 {
		t.Fatal("unmuted solo'd source inaudible")
	}
}

// TestPairGainAudible: the resolved pair gain actually reaches the
// rendered level, per pair — the same source at different channel gains
// for two listeners renders at different levels.
func TestPairGainAudible(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.AddSource("s", SourceConfig{TestTone: 300, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"loud", "quiet"} {
		if _, err := m.AddSink(id, &collectingWriter{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.SetRenderParams("s", withChannels([]string{"boosted", "cut"}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRenderParams("loud", withChannels(nil, []string{"boosted"})); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRenderParams("quiet", withChannels(nil, []string{"cut"})); err != nil {
		t.Fatal(err)
	}
	m.SetChannel("boosted", ChannelParams{Gain: 2.0})
	m.SetChannel("cut", ChannelParams{Gain: 0.5})
	settle(m, 10)

	ratio := math.Sqrt(listenerEnergy(m, "loud") / listenerEnergy(m, "quiet"))
	// Amplitude ratio should be 2.0/0.5 = 4 (same source, same geometry).
	if ratio < 3.6 || ratio > 4.4 {
		t.Fatalf("pair gain not per-pair: loud/quiet amplitude ratio = %v, want ~4", ratio)
	}
}
