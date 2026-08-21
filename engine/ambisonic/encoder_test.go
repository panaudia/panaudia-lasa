package ambisonic

import (
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

func mkEncoder(t *testing.T) *Encoder {
	t.Helper()
	setTestDelayModel(t) // NewEncoder is always dual-bus since M9.4
	cfg := common.MixerConfig{
		MaxNodes:     4,
		FrameSize:    8,
		ChannelCount: 9,
		Order:        2,
		Size:         2,
		ReverbPreset: common.REVERB_NONE,
	}
	return NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, 0)
}

// --- shouldIncludePeer veto matrix --------------------------------------

func TestShouldIncludePeerSelfExcluded(t *testing.T) {
	a := mkEncoder(t)
	if a.shouldIncludePeer(a) {
		t.Fatal("self must not be included as a peer")
	}
}

func TestShouldIncludePeerDefaultIncluded(t *testing.T) {
	a, b := mkEncoder(t), mkEncoder(t)
	if !a.shouldIncludePeer(b) {
		t.Fatal("default state: peer should be included")
	}
}

func TestShouldIncludePeerSpaceMutedVeto(t *testing.T) {
	a, b := mkEncoder(t), mkEncoder(t)
	b.SetSpaceMuted(true)
	if a.shouldIncludePeer(b) {
		t.Fatal("space-muted peer must be excluded")
	}
	b.SetSpaceMuted(false)
	if !a.shouldIncludePeer(b) {
		t.Fatal("after unmuting, peer should be included again")
	}
}

func TestShouldIncludePeerSpaceRoleMutedVeto(t *testing.T) {
	a, b := mkEncoder(t), mkEncoder(t)
	b.SetSpaceRoleMuted(true) // simulate BaseSpace.mutedRoles ∩ b.Roles ≠ ∅
	if a.shouldIncludePeer(b) {
		t.Fatal("peer with cached spaceRoleMuted=true must be excluded")
	}
}

func TestShouldIncludePeerPersonalMuteRoleVeto(t *testing.T) {
	a, b := mkEncoder(t), mkEncoder(t)
	a.AddMuteRole("performer")
	b.AddRole("performer")
	if a.shouldIncludePeer(b) {
		t.Fatal("listener mute-role intersecting peer.Roles must veto peer")
	}
	// Disjoint roles: not vetoed.
	a.RemoveMuteRole("performer")
	a.AddMuteRole("audience")
	if !a.shouldIncludePeer(b) {
		t.Fatal("disjoint mute-role must not veto")
	}
}

func TestShouldIncludePeerPersonalEntityMuteVeto(t *testing.T) {
	a, b := mkEncoder(t), mkEncoder(t)
	a.AddMute(b.Uuid)
	if a.shouldIncludePeer(b) {
		t.Fatal("listener-side personal mute must exclude peer")
	}
}

// --- solo wins over mute ------------------------------------------------

func TestShouldIncludePeerSoloBypassesAllMutes(t *testing.T) {
	a, b := mkEncoder(t), mkEncoder(t)
	// Stack every mute kind on b — solo must still win.
	b.SetSpaceMuted(true)
	b.SetSpaceRoleMuted(true)
	b.AddRole("performer")
	a.AddMuteRole("performer")
	a.AddMute(b.Uuid)

	a.AddSolo(b.Uuid)
	if !a.shouldIncludePeer(b) {
		t.Fatal("soloed peer must be included even with all mute vetoes set")
	}
}

func TestShouldIncludePeerSoloExcludesUnsoloed(t *testing.T) {
	a, b, c := mkEncoder(t), mkEncoder(t), mkEncoder(t)
	a.AddSolo(b.Uuid)
	if !a.shouldIncludePeer(b) {
		t.Fatal("soloed peer must be included")
	}
	if a.shouldIncludePeer(c) {
		t.Fatal("non-soloed peer must be excluded while solos active")
	}
}

// --- subspace gating is structural and applies even with solo -----------

func TestShouldIncludePeerSubspaceGatesEvenSolo(t *testing.T) {
	a, b := mkEncoder(t), mkEncoder(t)
	ssA, ssB := uuid.New(), uuid.New()
	a.AddSubSpace(ssA)
	b.AddSubSpace(ssB)
	a.AddSolo(b.Uuid)
	if a.shouldIncludePeer(b) {
		t.Fatal("disjoint subspaces should exclude even with solo")
	}
}

// --- gain composition ---------------------------------------------------

// Gain is an amplitude multiplier (2026-08-04; see recomputeGainFactor).
func gainFactorOf(gain float64) float32 {
	return float32(gain) * SQRT_4_PI
}

func TestSetGainRecomputesFactor(t *testing.T) {
	e := mkEncoder(t)
	e.SetGain(4.0)
	want := gainFactorOf(4.0)
	if e.gainFactor != want {
		t.Fatalf("SetGain(4.0): gainFactor = %v, want %v", e.gainFactor, want)
	}
}

func TestRoleGainMultiplierComposesMultiplicatively(t *testing.T) {
	e := mkEncoder(t)
	e.SetGain(2.0) // raw entity gain = 2.0
	e.SetRoleGainMultiplier(0.25)
	// effective gain = 2.0 * 0.25 = 0.5
	want := gainFactorOf(0.5)
	if e.gainFactor != want {
		t.Fatalf("composed gain: gainFactor = %v, want %v (effective gain 0.5)", e.gainFactor, want)
	}
}

func TestRoleGainMultiplierDefaultIsOne(t *testing.T) {
	e := mkEncoder(t)
	e.SetGain(3.0)
	// no role gain set: factor matches raw gain
	want := gainFactorOf(3.0)
	if e.gainFactor != want {
		t.Fatalf("no role gain: gainFactor = %v, want %v", e.gainFactor, want)
	}
}

func TestRoleGainMultiplierClearedRestoresEntityGain(t *testing.T) {
	e := mkEncoder(t)
	e.SetGain(2.0)
	e.SetRoleGainMultiplier(0.25)
	e.SetRoleGainMultiplier(1.0) // simulate role-gain tombstone
	want := gainFactorOf(2.0)
	if e.gainFactor != want {
		t.Fatalf("after clearing role gain: gainFactor = %v, want %v", e.gainFactor, want)
	}
}

// --- attenuation override (replace, not multiply) -----------------------

func TestSetAttenuationRecomputesExponent(t *testing.T) {
	e := mkEncoder(t)
	e.SetAttenuation(4.0)
	if e.peerAttenuationExponent != 2.0 {
		t.Fatalf("SetAttenuation(4): expected exponent 2.0, got %v", e.peerAttenuationExponent)
	}
}

func TestRoleAttenuationOverrideReplacesEntityValue(t *testing.T) {
	e := mkEncoder(t)
	e.SetAttenuation(2.0) // entity exponent 1.0
	override := 6.0
	e.SetRoleAttenuationOverride(&override)
	// override replaces, exponent = 6.0 / 2.0 = 3.0
	if e.peerAttenuationExponent != 3.0 {
		t.Fatalf("override: expected exponent 3.0, got %v", e.peerAttenuationExponent)
	}
}

func TestRoleAttenuationOverrideClearedRestoresEntityValue(t *testing.T) {
	e := mkEncoder(t)
	e.SetAttenuation(2.0)
	override := 6.0
	e.SetRoleAttenuationOverride(&override)
	e.SetRoleAttenuationOverride(nil) // tombstone
	if e.peerAttenuationExponent != 1.0 {
		t.Fatalf("after clearing override: expected exponent 1.0, got %v", e.peerAttenuationExponent)
	}
}
