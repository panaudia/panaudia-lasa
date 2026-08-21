package engine

// Channel audibility and the pair record — option D (2026-07-30) grown
// to Phase C completion (2026-08-04): include/exclude AND the per-pair
// scalars are resolved entirely in Layer 2, per (source, listener)
// pair, into slot-indexed matrices. The encoder reads its listener row
// per encode (one indexed load per pair); the DSP layer needs no
// policy knowledge.
//
// Three matrices, recomputed whole on any policy change (membership,
// channel table, render params, personal mute/solo, entity add/remove)
// — control-rate work, O(N²·channels) on the audio thread — and read
// per tick:
//
//	audible[k][s]    — s renders into k's mix at all
//	pairGain[k][s]   — composed weight factor: source GainFactor ×
//	                   winning channel gain (§4: highest wins, unset =
//	                   identity 1.0)
//	pairAttExp[k][s] — amplitude attenuation exponent: the lowest SET
//	                   channel attenuation REPLACES the source's own
//	                   (§4: unset = no override)
//
// Personal mute/solo (base profile §6, semantics decided 2026-08-04):
// mute is BIDIRECTIONAL — either side muting the other silences the
// pair both ways (the user expectation, matching the incumbent; the
// muter stops hearing AND being heard by the muted). Solo restricts
// the listener to its solo'd set and WINS over personal mutes in both
// directions. Space moderation mute is the encoder's own veto and is
// never bypassed (the encoder's legacy Solos set stays empty). Keyed
// by entity id, not encoder uuid: a muted entity that departs and
// rejoins keeps matching the owner's persistent mute key.

// markAudibleDirty is called inside change closures.
func (m *Mixer) markAudibleDirty() {
	m.audibleDirty = true
}

// resolvePair computes one (source s → listener k) cell: audibility per
// the §4 channel intersection and §6 mute/solo rules, and the §4 pair
// scalars. Audio thread only.
func (m *Mixer) resolvePair(s, k *entity) (audible bool, gainF float32, attExp float64) {
	// Shared unmuted channels; winner accumulation in the same pass.
	shared := false
	winGain := -1.0 // no candidate yet; every candidate is >= 0
	haveAtt := false
	winAtt := 0.0
	for _, c := range s.params.SourceChannels {
		cp, named := m.channelTable[c]
		if named && cp.Muted {
			continue
		}
		for _, kc := range k.params.SinkChannels {
			if kc != c {
				continue
			}
			shared = true
			g := 1.0 // unset counts as the identity candidate (§4)
			if named {
				g = cp.Gain
			}
			if g > winGain {
				winGain = g
			}
			if named && cp.Attenuation != nil && (!haveAtt || *cp.Attenuation < winAtt) {
				haveAtt, winAtt = true, *cp.Attenuation
			}
			break
		}
	}
	if !shared {
		return false, 0, 0
	}

	// Personal solo/mute (§6): solo defines the listener's audible set
	// and beats personal mutes; otherwise a mute on either side vetoes.
	if solos := m.personalSolos[k.id]; len(solos) > 0 {
		if !solos[s.id] {
			return false, 0, 0
		}
	} else if m.personalMutes[k.id][s.id] || m.personalMutes[s.id][k.id] {
		return false, 0, 0
	}

	gainF = s.enc.GainFactor() * float32(winGain)
	if haveAtt {
		attExp = winAtt / 2.0 // amplitude-domain exponent, as the encoder's own
	} else {
		attExp = s.enc.PeerAttenuationExponent()
	}
	return true, gainF, attExp
}

// recomputeAudible rebuilds the whole pair record. Self-pairs are
// computed like any other: self-exclusion is the encoder's own rule (and
// the future hears-self override will live there, not here).
func (m *Mixer) recomputeAudible() {
	m.audibleDirty = false
	for _, k := range m.ents {
		row, gRow, aRow := m.audible[k.slot], m.pairGain[k.slot], m.pairAttExp[k.slot]
		for _, s := range m.ents {
			row[s.slot], gRow[s.slot], aRow[s.slot] = m.resolvePair(s, k)
		}
	}
}
