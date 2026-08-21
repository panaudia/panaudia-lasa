package ambisonic

import (
	"math"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia-lasa/engine/common"
)

const SQRT_4_PI float32 = 3.5449077018110318

// NearFieldGainCap bounds the distance-law gain for close sources (the
// attenuation factor is 1 at 1 m and grows toward the head). Historically
// 1.0 (no boost inside 1 m); unpegged to 3.0 (+9.5 dB, reached at ~0.58 m
// at the default attenuation) after the M4–M6 listening pass — proximity
// now carries real level as well as the NFC spectral/ILD cues. Shared by
// the classic (pairGeometry) and bilateral (bilateralWeights) paths so the
// two render modes cannot drift; interacts with the loudness plan's
// headroom budget (plan/loudness/research.md) — revisit there, not here.
const NearFieldGainCap float32 = 3.0

type Encoder struct {
	Uuid     uuid.UUID
	Position common.Position
	// PositionMeters is Position scaled by the space size, converted once
	// at SetPosition (design decision 9, plan/near-field-compensation): the
	// geometry pass and everything below it work in meters and never see
	// the normalized representation.
	PositionMeters common.Position
	// listenerPosMeters / hasListenerPos: the listener-role position
	// override (engine-added split, see listener_position.go). Unset =
	// listener role reads PositionMeters, the historical behaviour.
	listenerPosMeters common.Position
	hasListenerPos    bool
	// rotation / headFrame: listener head rotation for encode-side
	// head-frame directions (bilateral M1). headFrame is rebuilt at
	// SetRotation time — at most once per tick — never per pair.
	rotation          common.Rotation
	headFrame         common.RotationMatrix33
	headFrameIdentity bool
	// dualBus (bilateral M2): Output carries two ear buses in one
	// contiguous buffer — bus L at [:busSamples], bus R at [busSamples:]
	// (open question 3 resolved: contiguous keeps WriteAmbisonic and the
	// split-mode wire format single-slice). Captured at construction so
	// buffer sizes and mix behavior can never disagree.
	dualBus    bool
	busSamples int
	// Encode-side ITD state (M4, delay.go). inputRing is this node's role
	// as a SOURCE: its recent input samples, shared by every listener.
	// The rest is this node's role as a LISTENER: per-peer-slot previous
	// delays for the in-frame ramp, generation-tracked so slot re-lease
	// self-invalidates.
	inputRing      []float32
	woodworthScale float64
	delayBase      float32
	prevDelayL     []float32
	prevDelayR     []float32
	slotGen        []uint64
	frameGen       uint64
	// slotOwner records which source's uuid this listener last rendered in
	// each peer slot. Slot re-lease is immediate and first-fit, so frame
	// adjacency alone (slotGen) cannot tell a new occupant from the old
	// one — the owner check zeroes the generations so the newcomer snaps
	// its delays and starts NFC from zero state.
	slotOwner []uuid.UUID
	// M6 near-field biquad state, per peer slot per ear (2 sections × 2
	// DF2T vars); nfcGen tracks which slots were NFC-processed last frame
	// so stale state is zeroed on (re-)entry — safe because at the gate
	// boundary the blended filter is identity with naturally zero state.
	nfcState                [][2][2 * nfcNumSections]float32
	nfcGen                  []uint64
	slot                    int
	hasInput                bool
	gain                    float64
	gainFactor              float32
	attenuation             float64
	peerAttenuationExponent float64
	Input                   []float32
	weightsBufferA          []float32
	weightsBufferB          []float32
	// Shared-input reverb (engine-added, shared_inputs.go): when
	// sharedInputs is set, the reverb bus GEMMs densely over the shared
	// slot matrix with these full-width transposed weight matrices
	// (double-buffered for the fade, zero columns masking exclusions).
	sharedInputs     *SharedInputs
	reverbSharedA    []float32
	reverbSharedB    []float32
	reverbSharedTemp []float32
	// Bus-R weight double-buffers (M5 parallax: near sources give each ear
	// its own weight vector; far sources copy L into R). nil flag-off.
	weightsBufferRA      []float32
	weightsBufferRB      []float32
	reverbWeightsBufferA []float32
	reverbWeightsBufferB []float32
	sphericalHarmonics   []float32
	bufferACurrent       bool
	ReverbOutput         []float32
	Output               []float32
	mixerConfig          common.MixerConfig
	reverb               *SimpleReverb

	SubSpaces mapset.Set[uuid.UUID]
	Mutes     mapset.Set[uuid.UUID]
	Solos     mapset.Set[uuid.UUID]

	// Roles this entity holds. Source-of-truth set, mirroring the
	// {uuid}.roles.{R} flat keys on the entity topic.
	Roles mapset.Set[string]
	// MuteRoles is the listener-side personal "mute by role" set
	// ({me}.mute-roles.{R}). Sources whose Roles intersect this set are
	// vetoed for *this* listener.
	MuteRoles mapset.Set[string]
	// SpaceMuted is true when {uuid}.muted is set on the entity topic
	// (space-wide entity mute on this source).
	SpaceMuted bool

	// Cached for fast-path use in shouldIncludePeer: true iff any role
	// in Roles is also in BaseSpace.mutedRoles. Maintained by
	// BaseSpace.RefreshEncoderRoleEffects.
	spaceRoleMuted bool
	// Cached role-derived multiplier on entity gain (default 1.0).
	roleGainMultiplier float64
	// Cached role-derived attenuation override; nil = no override.
	// When set, replaces (not multiplies) the entity attenuation.
	roleAttenuationOverride *float64
	// This listener's slot-indexed per-pair scalar rows (SetPairScalars);
	// nil = classic per-source values.
	pairGain   []float32
	pairAttExp []float64

	// Source render extent/directivity (LASA render.size / render.
	// directivity; engine plan/size-and-directivity.md, 2026-08-04).
	// size in metres (0 = point source), directivity 0..1 (cardioid
	// family), forward = the source's local +X in world coordinates,
	// refreshed per tick by the engine while directivity > 0.
	size        float64
	directivity float64
	forward     common.Position

	// headFrameSource marks a source whose position is a per-listener
	// HEAD-relative offset (LASA frame:"head", 2026-08-05): the pair
	// geometry consumes it verbatim — no world translation, no head
	// rotation — and classic (raw-ambisonic) renders exclude it, since
	// a world-frame field cannot carry a per-listener source. For such
	// a source, forward/size/directivity are head-relative too, and the
	// laws stay coherent (both vectors live in the listener's frame).
	headFrameSource bool

	filteredPeers []*Encoder
}

// NewEncoder builds a node encoder whose render mode follows the bilateral
// flag (binaural outputs). Raw-ambisonic outputs use NewClassicEncoder.
func NewEncoder(
	Uuid uuid.UUID,
	hasInput bool,
	gain float64,
	attenuation float64,
	mixerConfig common.MixerConfig,
	slot int) *Encoder {
	return newEncoder(Uuid, hasInput, gain, attenuation, mixerConfig, slot, true)
}

// NewClassicEncoder builds a single-mix world-frame encoder — for
// raw-ambisonic output nodes (ROC out, design decision 10, permanent):
// rotation is never applied at encode, inputs carry no ITD/parallax/NFC,
// and Output is one 16-ch field. Its input ring is still allocated so the
// node can serve as a SOURCE for bilateral listeners.
func NewClassicEncoder(
	Uuid uuid.UUID,
	hasInput bool,
	gain float64,
	attenuation float64,
	mixerConfig common.MixerConfig,
	slot int) *Encoder {
	return newEncoder(Uuid, hasInput, gain, attenuation, mixerConfig, slot, false)
}

func newEncoder(
	Uuid uuid.UUID,
	hasInput bool,
	gain float64,
	attenuation float64,
	mixerConfig common.MixerConfig,
	slot int,
	bilateral bool) *Encoder {

	encoder := Encoder{}
	encoder.gain = gain
	encoder.gainFactor = float32(gain) * SQRT_4_PI
	encoder.forward = common.Position{X: 1} // facing +X until told otherwise
	encoder.Uuid = Uuid
	encoder.slot = slot
	encoder.hasInput = hasInput
	encoder.attenuation = attenuation
	encoder.peerAttenuationExponent = attenuation / 2.0
	encoder.mixerConfig = mixerConfig
	encoder.reverb = NewSimpleReverb(mixerConfig.FrameSize, mixerConfig.ChannelCount, 48000.0)

	switch mixerConfig.ReverbPreset {
	case common.REVERB_TIGHT_ROOM:
		encoder.reverb.SetPresetTightRoom()
	case common.REVERB_SMALL_ROOM:
		encoder.reverb.SetPresetSmallRoom()
	case common.REVERB_MEDIUM_ROOM:
		encoder.reverb.SetPresetMediumRoom()
	case common.REVERB_LARGE_HALL:
		encoder.reverb.SetPresetLargeHall()
	case common.REVERB_CATHEDRAL:
		encoder.reverb.SetPresetCathedral()
	default:

	}

	// Thread-UNSAFE sets (engine-repo divergence, PROVENANCE.md): the
	// engine's phase discipline is that policy sets mutate only on the
	// audio thread while changes drain, and the Across phase only READS
	// them (concurrently, from render workers — concurrent map reads
	// with no writer are safe). The default mapset.NewSet wraps every
	// operation in an RWMutex, which shouldIncludePeer pays ~6 times
	// per (listener, source) pair per frame for nothing. The -race
	// suite is the standing verification of the discipline.
	encoder.SubSpaces = mapset.NewThreadUnsafeSet[uuid.UUID]()
	encoder.Mutes = mapset.NewThreadUnsafeSet[uuid.UUID]()
	encoder.Solos = mapset.NewThreadUnsafeSet[uuid.UUID]()
	encoder.Roles = mapset.NewThreadUnsafeSet[string]()
	encoder.MuteRoles = mapset.NewThreadUnsafeSet[string]()
	encoder.roleGainMultiplier = 1.0

	encoder.dualBus = bilateral
	encoder.busSamples = mixerConfig.FrameSize * mixerConfig.ChannelCount

	outputSamples := encoder.busSamples
	if encoder.dualBus {
		outputSamples *= 2
	}

	encoder.Input = make([]float32, mixerConfig.FrameSize)
	encoder.ReverbOutput = make([]float32, mixerConfig.FrameSize*common.REVERB_CHANNELS)
	encoder.Output = make([]float32, outputSamples)
	encoder.weightsBufferA = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
	encoder.weightsBufferB = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
	encoder.reverbWeightsBufferA = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
	encoder.reverbWeightsBufferB = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
	encoder.reverbSharedA = make([]float32, common.REVERB_CHANNELS*mixerConfig.MaxNodes)
	encoder.reverbSharedB = make([]float32, common.REVERB_CHANNELS*mixerConfig.MaxNodes)
	encoder.reverbSharedTemp = make([]float32, common.REVERB_CHANNELS*mixerConfig.FrameSize)
	encoder.bufferACurrent = true
	encoder.filteredPeers = make([]*Encoder, mixerConfig.MaxNodes)
	encoder.sphericalHarmonics = make([]float32, mixerConfig.ChannelCount)
	encoder.headFrameIdentity = true

	// The input ring is a SOURCE-side structure: any node's audio may
	// feed bilateral listeners' fractional-ITD reads, whatever this
	// node's own render mode is (classic raw-output nodes included).
	encoder.inputRing = make([]float32, DelayRingHistory+mixerConfig.FrameSize+1)
	if encoder.dualBus {
		encoder.initDelayModel()
		encoder.weightsBufferRA = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
		encoder.weightsBufferRB = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
		if encoder.ApplyReverb() {
			encoder.reverb.EnableDecorrelator()
		}
	}

	return &encoder
}

func (encoder *Encoder) SetPosition(position common.Position) {
	encoder.Position = position
	encoder.PositionMeters = common.Position{
		X: position.X * encoder.mixerConfig.Size,
		Y: position.Y * encoder.mixerConfig.Size,
		Z: position.Z * encoder.mixerConfig.Size,
	}
}

// SetRotation stores the listener's head rotation and rebuilds the
// head-frame matrix. Reached via the changes queue (doMoveNode) on the
// audio thread, at most once per tick.
func (encoder *Encoder) SetRotation(rotation common.Rotation) {
	encoder.rotation = rotation
	encoder.headFrameIdentity = rotation == (common.Rotation{})
	if !encoder.headFrameIdentity {
		encoder.headFrame = common.MatrixFromRotation(rotation)
	}
}

func (encoder *Encoder) ApplyReverb() bool {
	return encoder.mixerConfig.ReverbPreset != common.REVERB_NONE
}

func (encoder *Encoder) AddSubSpace(id uuid.UUID) {
	encoder.SubSpaces.Add(id)
}

func (encoder *Encoder) RemoveSubSpace(id uuid.UUID) {
	encoder.SubSpaces.Remove(id)
}

func (encoder *Encoder) AddSolo(id uuid.UUID) {
	encoder.Solos.Add(id)
}

func (encoder *Encoder) RemoveSolo(id uuid.UUID) {
	encoder.Solos.Remove(id)
}

func (encoder *Encoder) AddMute(id uuid.UUID) {
	encoder.Mutes.Add(id)
}

func (encoder *Encoder) RemoveMute(id uuid.UUID) {
	encoder.Mutes.Remove(id)
}

// SetGain replaces the entity's configured gain and recomputes gainFactor.
// Composes multiplicatively with the current cached roleGainMultiplier so
// any active role-gain stays applied across the change.
func (encoder *Encoder) SetGain(gain float64) {
	encoder.gain = gain
	encoder.recomputeGainFactor()
}

// SetAttenuation replaces the entity's configured attenuation and
// recomputes peerAttenuationExponent. If a roleAttenuationOverride is
// active it stays in effect (override, not compose).
func (encoder *Encoder) SetAttenuation(attenuation float64) {
	encoder.attenuation = attenuation
	encoder.recomputeAttenuationExponent()
}

func (encoder *Encoder) AddRole(role string) {
	encoder.Roles.Add(role)
}

func (encoder *Encoder) RemoveRole(role string) {
	encoder.Roles.Remove(role)
}

func (encoder *Encoder) AddMuteRole(role string) {
	encoder.MuteRoles.Add(role)
}

func (encoder *Encoder) RemoveMuteRole(role string) {
	encoder.MuteRoles.Remove(role)
}

func (encoder *Encoder) SetSpaceMuted(muted bool) {
	encoder.SpaceMuted = muted
}

// SetRoleGainMultiplier sets the cached min-of-applicable-role-gains
// multiplier (1.0 means "no role-gain overrides"). Recomputes gainFactor.
// Owned by BaseSpace.RefreshEncoderRoleEffects; not for direct app use.
func (encoder *Encoder) SetRoleGainMultiplier(m float64) {
	if m <= 0 {
		m = 1.0
	}
	encoder.roleGainMultiplier = m
	encoder.recomputeGainFactor()
}

// SetRoleAttenuationOverride installs (or clears) a role-derived
// attenuation override. Pass nil to clear. Recomputes peerAttenuationExponent.
func (encoder *Encoder) SetRoleAttenuationOverride(att *float64) {
	encoder.roleAttenuationOverride = att
	encoder.recomputeAttenuationExponent()
}

// SetSpaceRoleMuted is the cached "any of my roles is in BaseSpace.mutedRoles"
// flag. Maintained by BaseSpace.RefreshEncoderRoleEffects.
func (encoder *Encoder) SetSpaceRoleMuted(muted bool) {
	encoder.spaceRoleMuted = muted
}

// GainFactor returns the (composed) cached gain factor used during
// mixing. Read-only; callers must not mutate.
func (encoder *Encoder) GainFactor() float32 {
	return encoder.gainFactor
}

// SetPairScalars installs this LISTENER's slot-indexed per-pair rows
// (engine pair record, 2026-08-04): when set, encoding a peer reads
// pairGain[peer.slot] and pairAttExp[peer.slot] instead of the peer's
// own gainFactor/peerAttenuationExponent — per-(source,listener)
// channel gain/attenuation composition resolved upstream. nil (the
// default) keeps the classic per-source values; the rows are read on
// the audio thread only.
func (encoder *Encoder) SetPairScalars(pairGain []float32, pairAttExp []float64) {
	encoder.pairGain = pairGain
	encoder.pairAttExp = pairAttExp
}

// pairScalars resolves the gain factor and attenuation exponent for
// one peer: the pair-record row when installed, the peer's own values
// otherwise.
func (encoder *Encoder) pairScalars(peer *Encoder) (float32, float64) {
	if encoder.pairGain != nil {
		return encoder.pairGain[peer.slot], encoder.pairAttExp[peer.slot]
	}
	return peer.gainFactor, peer.peerAttenuationExponent
}

// SetSize sets the source's rendered extent in metres (0 = point).
func (encoder *Encoder) SetSize(size float64) { encoder.size = size }

// SetDirectivity sets the source's cardioid-family directivity (0 =
// omni, 1 = full cardioid).
func (encoder *Encoder) SetDirectivity(k float64) { encoder.directivity = k }

// SetSourceForward sets the source's facing (local +X) in world
// coordinates. Audio thread, per tick while directivity is active.
func (encoder *Encoder) SetSourceForward(f common.Position) { encoder.forward = f }

// SetHeadFrameSource marks/unmarks the source as head-frame (see the
// field comment). Set at entity construction, before rendering.
func (encoder *Encoder) SetHeadFrameSource(on bool) { encoder.headFrameSource = on }

// PeerAttenuationExponent returns the (override-aware) cached attenuation
// exponent used by GetWeights / GetWeightsForReverb.
func (encoder *Encoder) PeerAttenuationExponent() float64 {
	return encoder.peerAttenuationExponent
}

// SpaceRoleMuted reports whether the encoder is currently flagged as
// muted by space-wide role mute (any of its Roles ∈ BaseSpace.mutedRoles).
func (encoder *Encoder) SpaceRoleMuted() bool {
	return encoder.spaceRoleMuted
}

// recomputeGainFactor folds the composed gain into the cached weight
// factor. Gain is an AMPLITUDE multiplier (0.5 = −6 dB, 2.0 = +6 dB) —
// the LASA base profile's domain, decided 2026-08-04; the incumbent's
// power-domain √gain is a recorded divergence (PROVENANCE.md).
func (encoder *Encoder) recomputeGainFactor() {
	effective := encoder.gain * encoder.roleGainMultiplier
	if effective < 0 {
		effective = 0
	}
	encoder.gainFactor = float32(effective) * SQRT_4_PI
}

func (encoder *Encoder) recomputeAttenuationExponent() {
	att := encoder.attenuation
	if encoder.roleAttenuationOverride != nil {
		att = *encoder.roleAttenuationOverride
	}
	encoder.peerAttenuationExponent = att / 2.0
}

// EncodePeers renders this listener's mix(es). dryMixerR is the second
// worker mixer for the bilateral bus R (M4: the two buses' packed inputs
// genuinely differ — per-ear ITD reads); it is unused (may be nil) flag-off.
func (encoder *Encoder) EncodePeers(peers []*Encoder, dryMixer *Mixer, dryMixerR *Mixer, reverbMixer *Mixer) {

	//common.LogDebug("Peers: %d", len(peers))

	if encoder.dualBus {
		// The frame generation advances on EVERY flag-on frame, including
		// ones that render no peers: if the early returns below skipped it,
		// a resume after a mute/solo/empty gap would see the pre-gap frame
		// as "last frame" and apply stale delay ramps and NFC state against
		// a ring that kept advancing during the gap.
		encoder.frameGen++
	}

	if len(peers) == 0 {
		encoder.ClearOutput()
		return
	}

	encoder.bufferACurrent = !encoder.bufferACurrent
	channelCount := encoder.mixerConfig.ChannelCount

	currentWeightsBuffer := encoder.weightsBufferA
	currentWeightsBufferR := encoder.weightsBufferRA
	currentReverbWeightsBuffer := encoder.reverbWeightsBufferA
	previousWeightsBuffer := encoder.weightsBufferB
	previousWeightsBufferR := encoder.weightsBufferRB
	previousReverbWeightsBuffer := encoder.reverbWeightsBufferB

	if !encoder.bufferACurrent {
		currentWeightsBuffer = encoder.weightsBufferB
		currentWeightsBufferR = encoder.weightsBufferRB
		currentReverbWeightsBuffer = encoder.reverbWeightsBufferB
		previousWeightsBuffer = encoder.weightsBufferA
		previousWeightsBufferR = encoder.weightsBufferRA
		previousReverbWeightsBuffer = encoder.reverbWeightsBufferA
	}

	clear(currentWeightsBuffer)
	clear(currentWeightsBufferR)
	clear(currentReverbWeightsBuffer)

	// Shared-input reverb: the full-width weight matrices double-buffer
	// in step with the per-slot buffers; clearing the current one makes
	// zero the mask for every slot no included pair writes this frame.
	reverbSharedCur, reverbSharedPrev := encoder.reverbSharedA, encoder.reverbSharedB
	if !encoder.bufferACurrent {
		reverbSharedCur, reverbSharedPrev = encoder.reverbSharedB, encoder.reverbSharedA
	}
	if encoder.sharedInputs != nil && encoder.ApplyReverb() {
		clear(reverbSharedCur)
	}

	encoder.filteredPeers = encoder.filteredPeers[:0]

	for _, peer := range peers {
		if encoder.shouldIncludePeer(peer) {
			encoder.filteredPeers = append(encoder.filteredPeers, peer)
		}
	}

	peerCount := len(encoder.filteredPeers)

	//common.LogDebug("Filtered Peers: %d", peerCount)

	if peerCount == 0 {
		encoder.ClearOutput()
		return
	}

	dryMixer.Reset(peerCount)
	reverbMixer.Reset(peerCount)
	if encoder.dualBus {
		dryMixerR.Reset(peerCount)
	}

	// Head-frame transform for encode-side rotation (bilateral M1),
	// resolved once per frame — nil means world-frame directions: the
	// flag-off path, the identity-rotation fast path, AND classic
	// raw-output nodes (per-output render mode, decision 10 — a raw
	// ambisonic consumer must never receive a head-frame-rotated field).
	var headFrame *common.RotationMatrix33
	if encoder.dualBus && !encoder.headFrameIdentity {
		headFrame = &encoder.headFrame
	}

	for _, peer := range encoder.filteredPeers {

		//common.LogDebug("encoder.Position:  %v", encoder.Position)
		//common.LogDebug("peer.Position:  %v", peer.Position)
		//common.LogDebug("peer.slot:  %d", peer.slot)
		//common.LogDebug("peer.gainFactor:  %f", peer.gainFactor)
		//common.LogDebug("peer.peerAttenuationExponent %f", peer.peerAttenuationExponent)

		slotIndex := peer.slot * channelCount

		weightsView := currentWeightsBuffer[slotIndex : slotIndex+channelCount]
		previousWeightsView := previousWeightsBuffer[slotIndex : slotIndex+channelCount]

		if encoder.dualBus {
			// M4+M5: per-ear ITD and per-ear parallax directions at
			// encode, all from one geometry pass (bilateralWeights). The
			// fractional-delay read IS the pack copy (design decision 4);
			// both ears read the peer's shared ring at their own offsets
			// (decision 5); near sources (< ParallaxFarFieldM) get per-ear
			// weight vectors, far sources share one SH evaluation.
			weightsViewR := currentWeightsBufferR[slotIndex : slotIndex+channelCount]
			previousWeightsViewR := previousWeightsBufferR[slotIndex : slotIndex+channelCount]

			if encoder.slotOwner[peer.slot] != peer.Uuid {
				// Slot re-leased to a new source since this listener last
				// rendered it: the departed source's delay-ramp start and
				// NFC biquad tail must not color the newcomer's onset.
				// frameGen starts at 1, so generation 0 never reads as
				// "packed last frame".
				encoder.slotOwner[peer.slot] = peer.Uuid
				encoder.slotGen[peer.slot] = 0
				encoder.nfcGen[peer.slot] = 0
			}

			var wetView []float32
			if encoder.ApplyReverb() {
				wetView = currentReverbWeightsBuffer[slotIndex : slotIndex+common.REVERB_CHANNELS]
			}
			latSine, dist := encoder.bilateralWeights(peer, headFrame, weightsView, weightsViewR, wetView)
			if wetView != nil {
				// The reverb bus stays undelayed: late reverb is diffuse,
				// and per-bus decorrelation (PostMix) carries the
				// interaural cue.
				if encoder.sharedInputs != nil {
					encoder.scatterReverbWeights(reverbSharedCur, peer.slot, wetView)
				} else {
					previousReverbWeightsView := previousReverbWeightsBuffer[slotIndex : slotIndex+common.REVERB_CHANNELS]
					reverbMixer.AddInput(peer.Input, wetView, previousReverbWeightsView)
				}
			}

			prevL, prevR, dL, dR := encoder.pairEarDelays(peer.slot, latSine)
			var cL, cR [nfcNumSections][5]float32
			if nfcCoefficientsPair(dist, latSine, &cL, &cR) {
				// M6: near source — the per-ear near-field biquads are
				// fused into the same pack pass, right after the delay
				// line (decision 4). Right ear reads the table at the
				// mirrored angle (decision 3's one-table symmetry).
				if encoder.nfcGen[peer.slot] != encoder.frameGen-1 {
					encoder.nfcState[peer.slot] = [2][2 * nfcNumSections]float32{}
				}
				encoder.nfcGen[peer.slot] = encoder.frameGen
				dryMixer.AddInputDelayedNFC(peer.inputRing, prevL, dL,
					&cL, &encoder.nfcState[peer.slot][0], weightsView, previousWeightsView)
				dryMixerR.AddInputDelayedNFC(peer.inputRing, prevR, dR,
					&cR, &encoder.nfcState[peer.slot][1], weightsViewR, previousWeightsViewR)
			} else {
				dryMixer.AddInputDelayed(peer.inputRing, prevL, dL, weightsView, previousWeightsView)
				dryMixerR.AddInputDelayed(peer.inputRing, prevR, dR, weightsViewR, previousWeightsViewR)
			}

		} else if encoder.ApplyReverb() {

			// Ony use the first 4 channels for reverb
			reverbWeightsView := currentReverbWeightsBuffer[slotIndex : slotIndex+common.REVERB_CHANNELS]
			previousReverbWeightsView := previousReverbWeightsBuffer[slotIndex : slotIndex+common.REVERB_CHANNELS]

			pairGain, pairAtt := encoder.pairScalars(peer)
			GetWeightsForReverb(weightsView,
				reverbWeightsView,
				encoder.sphericalHarmonics,
				pairGain,
				pairAtt,
				encoder.ListenerPositionMeters(),
				peer.PositionMeters,
				headFrame,
				encoder.mixerConfig.Order,
				channelCount,
				peer.forward, peer.size, peer.directivity)

			if encoder.sharedInputs != nil {
				encoder.scatterReverbWeights(reverbSharedCur, peer.slot, reverbWeightsView)
			} else {
				reverbMixer.AddInput(peer.Input, reverbWeightsView, previousReverbWeightsView)
			}
			dryMixer.AddInput(peer.Input, weightsView, previousWeightsView)

		} else {
			pairGain, pairAtt := encoder.pairScalars(peer)
			GetWeights(weightsView,
				pairGain,
				pairAtt,
				encoder.ListenerPositionMeters(),
				peer.PositionMeters,
				headFrame,
				encoder.mixerConfig.Order,
				peer.forward, peer.size, peer.directivity)
			dryMixer.AddInput(peer.Input, weightsView, previousWeightsView)
		}
	}

	dryMixer.Mix(encoder.Output[:encoder.busSamples])
	if encoder.dualBus {
		dryMixerR.Mix(encoder.Output[encoder.busSamples:])
	}

	if encoder.ApplyReverb() {
		if encoder.sharedInputs != nil {
			encoder.mixReverbShared(reverbSharedCur, reverbSharedPrev)
		} else {
			reverbMixer.Mix(encoder.ReverbOutput)
		}
	}

	//common.LogDebug("encoder.Output: %v", encoder.Output)
}

func (encoder *Encoder) PostMix() {
	if encoder.ApplyReverb() {
		if encoder.dualBus {
			// One stateful reverb computation; bus R gets the velvet-noise
			// decorrelated wet signal (M4, design decision 7) — with
			// ear-aligned decode filters, identical wet in both buses would
			// collapse the reverb image to the interaural midline.
			encoder.reverb.processWet(encoder.ReverbOutput)
			encoder.reverb.mixInto(encoder.ReverbOutput, encoder.Output[:encoder.busSamples])
			encoder.reverb.mixIntoDecorrelated(encoder.ReverbOutput, encoder.Output[encoder.busSamples:])
		} else {
			encoder.reverb.Apply(encoder.ReverbOutput, encoder.Output)
		}
	}
}

// DualBus reports whether Output carries two ear buses (bilateral M2+);
// BusSamples is the per-bus length in floats (bus L = Output[:BusSamples]).
func (encoder *Encoder) DualBus() bool   { return encoder.dualBus }
func (encoder *Encoder) BusSamples() int { return encoder.busSamples }

func (encoder *Encoder) shouldIncludePeer(peer *Encoder) bool {

	if peer.Uuid == encoder.Uuid {
		return false
	}

	// A head-frame source is per-listener content: representable only in
	// a listener-anchored (bilateral) render, never in a world-frame
	// classic field (2026-08-05; the same principle as decision 10's
	// no-rotation rule for raw outputs).
	if peer.headFrameSource && !encoder.dualBus {
		return false
	}

	// Subspace visibility is structural, not a mute. It applies regardless
	// of solo (a soloed source still has to be in a shared subspace).
	if !encoder.SubSpaces.IsEmpty() || !peer.SubSpaces.IsEmpty() {
		if !encoder.SubSpaces.ContainsAnyElement(peer.SubSpaces) {
			return false
		}
	}

	// Solo wins over every mute. If the listener has any solos active, only
	// soloed peers reach them — and a soloed peer bypasses every mute veto.
	if !encoder.Solos.IsEmpty() {
		// ContainsOne, not Contains: the variadic signature heap-allocates
		// its argument slice on every call — two allocations per pair per
		// frame on the render hot path (found by the engine's
		// TestProcessAllocs guard; engine-repo divergence, PROVENANCE.md).
		if !encoder.Solos.ContainsOne(peer.Uuid) {
			return false
		}
		return true
	}

	// Mute vetoes (any of these excludes the peer).
	if peer.SpaceMuted {
		return false
	}
	if peer.spaceRoleMuted {
		return false
	}
	if !encoder.MuteRoles.IsEmpty() && !peer.Roles.IsEmpty() {
		if encoder.MuteRoles.ContainsAnyElement(peer.Roles) {
			return false
		}
	}
	if encoder.Mutes.ContainsOne(peer.Uuid) || peer.Mutes.ContainsOne(encoder.Uuid) {
		return false
	}

	return true
}

func (encoder *Encoder) ClearSource() {

	for i := range encoder.Input {
		encoder.Input[i] = 0
	}
}

func (encoder *Encoder) ClearOutput() {

	for i := range encoder.Output {
		encoder.Output[i] = 0
	}
}

func (encoder *Encoder) AddOtherSource(other *Encoder) {

	for i := range encoder.Input {
		encoder.Input[i] = encoder.Input[i] + other.Input[i]
	}
}

func (encoder *Encoder) AddOtherSink(other *Encoder) {

	for i := range encoder.ReverbOutput {
		encoder.ReverbOutput[i] = encoder.ReverbOutput[i] + other.ReverbOutput[i]
	}

	for i := range encoder.Output {
		encoder.Output[i] = encoder.Output[i] + other.Output[i]
	}
}

func (encoder *Encoder) AddGlobalBuffer(globalBuffer []float32) {

	if encoder.ApplyReverb() {
		for i := range globalBuffer {
			encoder.ReverbOutput[i] = encoder.ReverbOutput[i] + globalBuffer[i]
		}
	} else {
		for i := range globalBuffer {
			encoder.Output[i] = encoder.Output[i] + globalBuffer[i]
		}
		if encoder.dualBus {
			busR := encoder.Output[encoder.busSamples:]
			for i := range globalBuffer {
				busR[i] = busR[i] + globalBuffer[i]
			}
		}
	}
}

// pairGeometry is the single per-pair geometry pass (bilateral M1):
// meters-native (design decision 9 — positions arrive pre-converted),
// one distance/attenuation computation per pair, direction optionally
// rotated into the listener's head frame. M5 extends this same pass to
// emit both ears' directions. The extent/directivity laws (extent.go)
// evaluate on the world-frame direction, before the head rotation.
func pairGeometry(listenerPosMeters common.Position,
	peerPosMeters common.Position,
	headFrame *common.RotationMatrix33,
	peerGainFactor float32,
	peerAttenuationExponent float64,
	forward common.Position,
	size, directivity float64) (norm common.Position, distanceMeters float64, nodeGain float32) {

	norm, distanceMeters = common.TrigCartesianNormRectAndDistance(listenerPosMeters, peerPosMeters)

	nodeGain = peerGainFactor
	if size > 0 || directivity > 0 { // extent gate: neutral pairs pay nothing
		nodeGain = peerGainFactor *
			distanceGain(surfaceDistance(distanceMeters, size), peerAttenuationExponent) *
			directivityFactor(forward, norm, 1.0, directivity) // norm is unit: pass |rel| = 1
	} else {
		nodeGain *= distanceGain(distanceMeters, peerAttenuationExponent)
	}

	if headFrame != nil {
		norm = headFrame.Apply(norm)
	}

	return norm, distanceMeters, nodeGain
}

// distanceGain is THE distance-attenuation law — 1 at 1 m, growing toward
// the head up to NearFieldGainCap — shared by the classic (pairGeometry)
// and bilateral (bilateralWeights) render paths so the two cannot drift.
func distanceGain(distanceMeters float64, exponent float64) float32 {
	var attenuationFactor float32
	if exponent == 1 {
		attenuationFactor = float32(1 / distanceMeters)
	} else {
		attenuationFactor = float32(math.Pow(1/distanceMeters, exponent))
	}
	if attenuationFactor > NearFieldGainCap {
		attenuationFactor = NearFieldGainCap
	}
	return attenuationFactor
}

// reverbSplit is THE dry/wet law for the reverb bus, shared by the classic
// (GetWeightsForReverb) and bilateral (bilateralWeights) paths. Both gains
// multiply the distance-attenuated node gain; only the reverb-carrying
// first REVERB_CHANNELS of the dry weights take dryGain. A source's size
// nudges the split toward wet (half its radius acts as extra distance):
// large sources get genuinely decorrelated energy through the reverb bus
// at zero marginal DSP cost (plan/size-and-directivity.md, kept modest).
func reverbSplit(distanceMeters, size float64) (dryGain, wetGain float32) {
	reverbGain := smoothstep(1.0, 8.0, distanceMeters+size*0.5) + 0.05
	return float32(1 - reverbGain), float32(reverbGain * 0.9)
}

// GetWeights fills the SH weight vector for the classic single-mix path.
// Single per-pair geometry pass: direction and attenuation come from one
// pairGeometry call. (The bilateral path uses bilateralWeights instead.)
func GetWeights(weights []float32,
	peerGainFactor float32,
	peerAttenuationExponent float64,
	listenerPosMeters common.Position,
	peerPosMeters common.Position,
	headFrame *common.RotationMatrix33,
	order int,
	forward common.Position,
	size, directivity float64) {

	norm, distanceMeters, nodeGain := pairGeometry(listenerPosMeters, peerPosMeters, headFrame,
		peerGainFactor, peerAttenuationExponent, forward, size, directivity)

	GetSphericalHarmonicsGained(order, float32(norm.X), float32(norm.Y), float32(norm.Z), nodeGain, weights)

	if size > 0 { // extent gate
		var ow [spreadMaxOrder + 1]float32
		if spreadOrderWeights(size, distanceMeters, order, &ow) {
			applyOrderWeights(order, &ow, weights)
		}
	}
}

// GetWeightsForReverb mirrors GetWeights and additionally splits dry/wet
// weights for the reverb bus (reverbSplit — the law shared with the
// bilateral path).
func GetWeightsForReverb(dryWeights []float32,
	wetWeights []float32,
	sphericalHarmonics []float32,
	peerGainFactor float32,
	peerAttenuationExponent float64,
	listenerPosMeters common.Position,
	peerPosMeters common.Position,
	headFrame *common.RotationMatrix33,
	order int,
	channels int,
	forward common.Position,
	size, directivity float64) {

	norm, distanceMeters, nodeGain := pairGeometry(listenerPosMeters, peerPosMeters, headFrame,
		peerGainFactor, peerAttenuationExponent, forward, size, directivity)

	dryGain, wetGain := reverbSplit(distanceMeters, size)

	GetSphericalHarmonics(order, float32(norm.X), float32(norm.Y), float32(norm.Z), sphericalHarmonics)
	if size > 0 { // extent gate
		var ow [spreadMaxOrder + 1]float32
		if spreadOrderWeights(size, distanceMeters, order, &ow) {
			applyOrderWeights(order, &ow, sphericalHarmonics[:channels])
		}
	}

	//only fill in the first 4 channels for reverb
	for i := 0; i < common.REVERB_CHANNELS; i++ {
		dryWeights[i] = sphericalHarmonics[i] * nodeGain * dryGain
		wetWeights[i] = sphericalHarmonics[i] * nodeGain * wetGain
	}

	for i := common.REVERB_CHANNELS; i < channels; i++ {
		dryWeights[i] = sphericalHarmonics[i] * nodeGain
	}
}

func smoothstep(edge0, edge1, x float64) float64 {
	t := (x - edge0) / (edge1 - edge0)

	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}

	return t * t * (3 - 2*t)
}

// Constants moved outside function to avoid recreating them
const (
	// Order 0
	SH_0_0 = 0.2820947917738781

	// Order 1
	SH_1_COEF = 0.48860251190292
	SH_1_0    = 0.4886025119029199

	// Order 2
	SH_2_COEF   = 0.5462742152960395
	SH_2_1_COEF = 1.092548430592079
	SH_2_0_A    = 0.9461746957575601
	SH_2_0_B    = 0.31539156525252

	// Order 3
	SH_3_COEF_F   = 0.5900435899266435
	SH_3_COEF_E   = 1.445305721320277
	SH_3_COEF_D_A = 2.285228997322329
	SH_3_COEF_D_B = 0.4570457994644658
	SH_3_0_A      = 1.865881662950577
	SH_3_0_B      = 1.119528997770346

	// Order 4
	SH_4_COEF     = 0.6258357354491763
	SH_4_1_COEF   = 1.770130769779931
	SH_4_2_COEF_A = 3.31161143515146
	SH_4_2_COEF_B = 0.47308734787878
	SH_4_3_COEF_A = 4.683325804901025
	SH_4_3_COEF_B = 2.007139630671868
	SH_4_0_A      = 1.984313483298443
	SH_4_0_B      = 1.006230589874905

	// Order 5
	SH_5_COEF     = 0.6563820568401703
	SH_5_1_COEF   = 2.075662314881041
	SH_5_4_COEF_A = 1.98997487421324
	SH_5_4_COEF_B = 1.002853072844814
	SH_5_3_TMP_A  = 2.03100960115899
	SH_5_3_TMP_B  = 0.991031208965115
	SH_5_2_TMP_A  = 7.190305177459987
	SH_5_2_TMP_B  = 2.396768392486662
	SH_5_1_TMP_A  = 4.403144694917254
	SH_5_1_TMP_B  = 0.4892382994352505
)

//func GetSphericalHarmonicsGained(order int, nx, ny, nz, gain float32, weights []float32) {
//
//	if order == 4 {
//		GetSphericalHarmonics4Gained(nx, ny, nz, gain, weights)
//		return
//	}
//
//	if order == 5 {
//		GetSphericalHarmonics5Gained(nx, ny, nz, gain, weights)
//		return
//	}
//
//	// Precompute commonly used values
//	nx2 := nx * nx
//	ny2 := ny * ny
//	nz2 := nz * nz
//
//	// Order 0 (omnidirectional)
//	weights[0] = SH_0_0 * gain
//
//	// Order 1 (dipole patterns)
//	gain1 := SH_1_COEF * gain
//	weights[1] = gain1 * ny
//	weights[2] = SH_1_0 * nz * gain
//	weights[3] = gain1 * nx
//
//	// Order 2 (quadrupole patterns)
//	fC1 := nx2 - ny2
//	fS1 := 2.0 * nx * ny
//
//	gain2Coef := SH_2_COEF * gain
//	gain21CoefNz := SH_2_1_COEF * nz * gain
//
//	weights[4] = gain2Coef * fS1
//	weights[5] = gain21CoefNz * ny
//	weights[6] = (nz2*SH_2_0_A - SH_2_0_B) * gain
//	weights[7] = gain21CoefNz * nx
//	weights[8] = gain2Coef * fC1
//
//	// Order 3 (octupole patterns)
//	if order > 2 {
//		fTmpD := nz2*SH_3_COEF_D_A - SH_3_COEF_D_B
//		fTmpE := SH_3_COEF_E * nz
//		gain3CoefF := SH_3_COEF_F * gain
//		gain3TmpE := fTmpE * gain
//		gain3TmpD := fTmpD * gain
//
//		// Y(3,-3): involves (x*sin(2φ) + y*cos(2φ)) = x*fS1 + y*fC1
//		weights[9] = gain3CoefF * (nx*fS1 + ny*fC1)
//		// Y(3,-2): z * sin(2φ)
//		weights[10] = gain3TmpE * fS1
//		// Y(3,-1): y * (polynomial in z²)
//		weights[11] = gain3TmpD * ny
//		// Y(3,0): z * (polynomial in z²)
//		weights[12] = nz * (nz2*SH_3_0_A - SH_3_0_B) * gain
//		// Y(3,1): x * (polynomial in z²)
//		weights[13] = gain3TmpD * nx
//		// Y(3,2): z * cos(2φ)
//		weights[14] = gain3TmpE * fC1
//		// Y(3,3): involves (x*cos(2φ) - y*sin(2φ)) = x*fC1 - y*fS1
//		weights[15] = gain3CoefF * (nx*fC1 - ny*fS1)
//	}
//}

func GetSphericalHarmonicsGained(order int, nx, ny, nz, gain float32, weights []float32) {
	GetSphericalHarmonics(order, nx, ny, nz, weights)
	for i := 0; i < len(weights); i++ {
		weights[i] *= gain
	}
}

func GetSphericalHarmonics(order int, nx, ny, nz float32, weights []float32) {

	if order == 4 {
		GetSphericalHarmonics4(nx, ny, nz, weights)
		return
	}

	if order == 5 {
		GetSphericalHarmonics5(nx, ny, nz, weights)
		return
	}

	// Precompute commonly used values
	nx2 := nx * nx
	ny2 := ny * ny
	nz2 := nz * nz

	// Order 0 (omnidirectional)
	weights[0] = SH_0_0

	// Order 1 (dipole patterns)
	//gain1 := SH_1_COEF
	weights[1] = SH_1_COEF * ny
	weights[2] = SH_1_0 * nz
	weights[3] = SH_1_COEF * nx

	// Order 2 (quadrupole patterns)
	fC1 := nx2 - ny2
	fS1 := 2.0 * nx * ny

	Coef21Nz := SH_2_1_COEF * nz

	weights[4] = SH_2_COEF * fS1
	weights[5] = Coef21Nz * ny
	weights[6] = (nz2*SH_2_0_A - SH_2_0_B)
	weights[7] = Coef21Nz * nx
	weights[8] = SH_2_COEF * fC1

	// Order 3 (octupole patterns)
	if order > 2 {
		fTmpD := nz2*SH_3_COEF_D_A - SH_3_COEF_D_B
		fTmpE := SH_3_COEF_E * nz
		// Y(3,-3): involves (x*sin(2φ) + y*cos(2φ)) = x*fS1 + y*fC1
		weights[9] = SH_3_COEF_F * (nx*fS1 + ny*fC1)
		// Y(3,-2): z * sin(2φ)
		weights[10] = fTmpE * fS1
		// Y(3,-1): y * (polynomial in z²)
		weights[11] = fTmpD * ny
		// Y(3,0): z * (polynomial in z²)
		weights[12] = nz * (nz2*SH_3_0_A - SH_3_0_B)
		// Y(3,1): x * (polynomial in z²)
		weights[13] = fTmpD * nx
		// Y(3,2): z * cos(2φ)
		weights[14] = fTmpE * fC1
		// Y(3,3): involves (x*cos(2φ) - y*sin(2φ)) = x*fC1 - y*fS1
		weights[15] = SH_3_COEF_F * (nx*fC1 - ny*fS1)
	}
}

//
//// SHEval4 calculates spherical harmonics of order 4
//func GetSphericalHarmonics4Gained(fX, fY, fZ, gain float32, pSH []float32) {
//	var fC0, fC1, fS0, fS1 float32
//	fZ2 := fZ * fZ
//
//	pSH[0] = SH_0_0 * gain
//	pSH[2] = SH_1_0 * fZ * gain
//
//	temp6 := SH_2_0_A*fZ2 - SH_2_0_B
//	temp12 := fZ * (SH_3_0_A*fZ2 - SH_3_0_B)
//	pSH[6] = temp6 * gain
//	pSH[12] = temp12 * gain
//	pSH[20] = (SH_4_0_A*fZ*temp12 - SH_4_0_B*temp6) * gain
//
//	fC0 = fX
//	fS0 = fY
//
//	// Order 1 terms
//	pSH[3] = SH_1_COEF * fC0 * gain
//	pSH[1] = SH_1_COEF * fS0 * gain
//
//	// Order 2 terms with Z
//	gainZ := SH_2_1_COEF * fZ * gain
//	pSH[7] = gainZ * fC0
//	pSH[5] = gainZ * fS0
//
//	// Order 3 polynomial in Z
//	gainZ2 := (SH_3_COEF_D_A*fZ2 - SH_3_COEF_D_B) * gain
//	pSH[13] = gainZ2 * fC0
//	pSH[11] = gainZ2 * fS0
//
//	// Order 4 polynomial in Z
//	gainZ3 := fZ * (SH_4_3_COEF_A*fZ2 - SH_4_3_COEF_B) * gain
//	pSH[21] = gainZ3 * fC0
//	pSH[19] = gainZ3 * fS0
//
//	fC1 = fX*fC0 - fY*fS0
//	fS1 = fX*fS0 + fY*fC0
//
//	// Order 2 terms
//	pSH[8] = SH_2_COEF * fC1 * gain
//	pSH[4] = SH_2_COEF * fS1 * gain
//
//	// Order 3 terms with Z
//	gainZ = SH_3_COEF_E * fZ * gain
//	pSH[14] = gainZ * fC1
//	pSH[10] = gainZ * fS1
//
//	// Order 4 polynomial in Z
//	gainZ2 = (SH_4_2_COEF_A*fZ2 - SH_4_2_COEF_B) * gain
//	pSH[22] = gainZ2 * fC1
//	pSH[18] = gainZ2 * fS1
//
//	fC0 = fX*fC1 - fY*fS1
//	fS0 = fX*fS1 + fY*fC1
//
//	// Order 3 terms
//	pSH[15] = SH_3_COEF_F * fC0 * gain
//	pSH[9] = SH_3_COEF_F * fS0 * gain
//
//	// Order 4 terms with Z
//	gainZ = SH_4_1_COEF * fZ * gain
//	pSH[23] = gainZ * fC0
//	pSH[17] = gainZ * fS0
//
//	fC1 = fX*fC0 - fY*fS0
//	fS1 = fX*fS0 + fY*fC0
//
//	// Final Order 4 terms
//	pSH[24] = SH_4_COEF * fC1 * gain
//	pSH[16] = SH_4_COEF * fS1 * gain
//}

//// SHEval5 calculates spherical harmonics of order 5
//func GetSphericalHarmonics5Gained(fX, fY, fZ, gain float32, pSH []float32) {
//	var fC0, fC1, fS0, fS1, fTmpA, fTmpB, fTmpC float32
//	fZ2 := fZ * fZ
//
//	pSH[0] = SH_0_0 * gain
//	pSH[2] = SH_1_0 * fZ * gain
//
//	temp6 := SH_2_0_A*fZ2 + -SH_2_0_B
//	temp12 := fZ * (SH_3_0_A*fZ2 + -SH_3_0_B)
//	pSH[6] = temp6 * gain
//	pSH[12] = temp12 * gain
//
//	temp20 := SH_4_0_A*fZ*temp12 + -SH_4_0_B*temp6
//	pSH[20] = temp20 * gain
//	pSH[30] = (SH_5_4_COEF_A*fZ*temp20 + -SH_5_4_COEF_B*temp12) * gain
//	fC0 = fX
//	fS0 = fY
//
//	pSH[3] = SH_1_COEF * fC0 * gain
//	pSH[1] = SH_1_COEF * fS0 * gain
//	fTmpB = SH_2_1_COEF * fZ
//	pSH[7] = fTmpB * fC0 * gain
//	pSH[5] = fTmpB * fS0 * gain
//	fTmpC = SH_3_COEF_D_A*fZ2 + -SH_3_COEF_D_B
//	pSH[13] = fTmpC * fC0 * gain
//	pSH[11] = fTmpC * fS0 * gain
//	fTmpA = fZ * (SH_4_3_COEF_A*fZ2 + -SH_4_3_COEF_B)
//	pSH[21] = fTmpA * fC0 * gain
//	pSH[19] = fTmpA * fS0 * gain
//	fTmpB = SH_5_3_TMP_A*fZ*fTmpA + -SH_5_3_TMP_B*fTmpC
//	pSH[31] = fTmpB * fC0 * gain
//	pSH[29] = fTmpB * fS0 * gain
//	fC1 = fX*fC0 - fY*fS0
//	fS1 = fX*fS0 + fY*fC0
//
//	pSH[8] = SH_2_COEF * fC1 * gain
//	pSH[4] = SH_2_COEF * fS1 * gain
//	fTmpB = SH_3_COEF_E * fZ
//	pSH[14] = fTmpB * fC1 * gain
//	pSH[10] = fTmpB * fS1 * gain
//	fTmpC = SH_4_2_COEF_A*fZ2 + -SH_4_2_COEF_B
//	pSH[22] = fTmpC * fC1 * gain
//	pSH[18] = fTmpC * fS1 * gain
//	fTmpA = fZ * (SH_5_2_TMP_A*fZ2 + -SH_5_2_TMP_B)
//	pSH[32] = fTmpA * fC1 * gain
//	pSH[28] = fTmpA * fS1 * gain
//	fC0 = fX*fC1 - fY*fS1
//	fS0 = fX*fS1 + fY*fC1
//
//	pSH[15] = SH_3_COEF_F * fC0 * gain
//	pSH[9] = SH_3_COEF_F * fS0 * gain
//	fTmpB = SH_4_1_COEF * fZ
//	pSH[23] = fTmpB * fC0 * gain
//	pSH[17] = fTmpB * fS0 * gain
//	fTmpC = SH_5_1_TMP_A*fZ2 + -SH_5_1_TMP_B
//	pSH[33] = fTmpC * fC0 * gain
//	pSH[27] = fTmpC * fS0 * gain
//	fC1 = fX*fC0 - fY*fS0
//	fS1 = fX*fS0 + fY*fC0
//
//	pSH[24] = SH_4_COEF * fC1 * gain
//	pSH[16] = SH_4_COEF * fS1 * gain
//
//	fTmpB = SH_5_1_COEF * fZ
//	pSH[34] = fTmpB * fC1 * gain
//	pSH[26] = fTmpB * fS1 * gain
//	fC0 = fX*fC1 - fY*fS1
//	fS0 = fX*fS1 + fY*fC1
//
//	pSH[35] = SH_5_COEF * fC0 * gain
//	pSH[25] = SH_5_COEF * fS0 * gain
//}

// SHEval4 calculates spherical harmonics of order 4
func GetSphericalHarmonics4(fX, fY, fZ float32, pSH []float32) {
	var fC0, fC1, fS0, fS1 float32
	fZ2 := fZ * fZ

	pSH[0] = SH_0_0
	pSH[2] = SH_1_0 * fZ

	temp6 := SH_2_0_A*fZ2 - SH_2_0_B
	temp12 := fZ * (SH_3_0_A*fZ2 - SH_3_0_B)
	pSH[6] = temp6
	pSH[12] = temp12
	pSH[20] = (SH_4_0_A*fZ*temp12 - SH_4_0_B*temp6)

	fC0 = fX
	fS0 = fY

	// Order 1 terms
	pSH[3] = SH_1_COEF * fC0
	pSH[1] = SH_1_COEF * fS0

	// Order 2 terms with Z
	gainZ := SH_2_1_COEF * fZ
	pSH[7] = gainZ * fC0
	pSH[5] = gainZ * fS0

	// Order 3 polynomial in Z
	gainZ2 := (SH_3_COEF_D_A*fZ2 - SH_3_COEF_D_B)
	pSH[13] = gainZ2 * fC0
	pSH[11] = gainZ2 * fS0

	// Order 4 polynomial in Z
	gainZ3 := fZ * (SH_4_3_COEF_A*fZ2 - SH_4_3_COEF_B)
	pSH[21] = gainZ3 * fC0
	pSH[19] = gainZ3 * fS0

	fC1 = fX*fC0 - fY*fS0
	fS1 = fX*fS0 + fY*fC0

	// Order 2 terms
	pSH[8] = SH_2_COEF * fC1
	pSH[4] = SH_2_COEF * fS1

	// Order 3 terms with Z
	gainZ = SH_3_COEF_E * fZ
	pSH[14] = gainZ * fC1
	pSH[10] = gainZ * fS1

	// Order 4 polynomial in Z
	gainZ2 = (SH_4_2_COEF_A*fZ2 - SH_4_2_COEF_B)
	pSH[22] = gainZ2 * fC1
	pSH[18] = gainZ2 * fS1

	fC0 = fX*fC1 - fY*fS1
	fS0 = fX*fS1 + fY*fC1

	// Order 3 terms
	pSH[15] = SH_3_COEF_F * fC0
	pSH[9] = SH_3_COEF_F * fS0

	// Order 4 terms with Z
	gainZ = SH_4_1_COEF * fZ
	pSH[23] = gainZ * fC0
	pSH[17] = gainZ * fS0

	fC1 = fX*fC0 - fY*fS0
	fS1 = fX*fS0 + fY*fC0

	// Final Order 4 terms
	pSH[24] = SH_4_COEF * fC1
	pSH[16] = SH_4_COEF * fS1
}

// SHEval5 calculates spherical harmonics of order 5
func GetSphericalHarmonics5(fX, fY, fZ float32, pSH []float32) {
	var fC0, fC1, fS0, fS1, fTmpA, fTmpB, fTmpC float32
	fZ2 := fZ * fZ

	pSH[0] = SH_0_0
	pSH[2] = SH_1_0 * fZ

	temp6 := SH_2_0_A*fZ2 + -SH_2_0_B
	temp12 := fZ * (SH_3_0_A*fZ2 + -SH_3_0_B)
	pSH[6] = temp6
	pSH[12] = temp12

	temp20 := SH_4_0_A*fZ*temp12 + -SH_4_0_B*temp6
	pSH[20] = temp20
	pSH[30] = (SH_5_4_COEF_A*fZ*temp20 + -SH_5_4_COEF_B*temp12)
	fC0 = fX
	fS0 = fY

	pSH[3] = SH_1_COEF * fC0
	pSH[1] = SH_1_COEF * fS0
	fTmpB = SH_2_1_COEF * fZ
	pSH[7] = fTmpB * fC0
	pSH[5] = fTmpB * fS0
	fTmpC = SH_3_COEF_D_A*fZ2 + -SH_3_COEF_D_B
	pSH[13] = fTmpC * fC0
	pSH[11] = fTmpC * fS0
	fTmpA = fZ * (SH_4_3_COEF_A*fZ2 + -SH_4_3_COEF_B)
	pSH[21] = fTmpA * fC0
	pSH[19] = fTmpA * fS0
	fTmpB = SH_5_3_TMP_A*fZ*fTmpA + -SH_5_3_TMP_B*fTmpC
	pSH[31] = fTmpB * fC0
	pSH[29] = fTmpB * fS0
	fC1 = fX*fC0 - fY*fS0
	fS1 = fX*fS0 + fY*fC0

	pSH[8] = SH_2_COEF * fC1
	pSH[4] = SH_2_COEF * fS1
	fTmpB = SH_3_COEF_E * fZ
	pSH[14] = fTmpB * fC1
	pSH[10] = fTmpB * fS1
	fTmpC = SH_4_2_COEF_A*fZ2 + -SH_4_2_COEF_B
	pSH[22] = fTmpC * fC1
	pSH[18] = fTmpC * fS1
	fTmpA = fZ * (SH_5_2_TMP_A*fZ2 + -SH_5_2_TMP_B)
	pSH[32] = fTmpA * fC1
	pSH[28] = fTmpA * fS1
	fC0 = fX*fC1 - fY*fS1
	fS0 = fX*fS1 + fY*fC1

	pSH[15] = SH_3_COEF_F * fC0
	pSH[9] = SH_3_COEF_F * fS0
	fTmpB = SH_4_1_COEF * fZ
	pSH[23] = fTmpB * fC0
	pSH[17] = fTmpB * fS0
	fTmpC = SH_5_1_TMP_A*fZ2 + -SH_5_1_TMP_B
	pSH[33] = fTmpC * fC0
	pSH[27] = fTmpC * fS0
	fC1 = fX*fC0 - fY*fS0
	fS1 = fX*fS0 + fY*fC0

	pSH[24] = SH_4_COEF * fC1
	pSH[16] = SH_4_COEF * fS1

	fTmpB = SH_5_1_COEF * fZ
	pSH[34] = fTmpB * fC1
	pSH[26] = fTmpB * fS1
	fC0 = fX*fC1 - fY*fS1
	fS0 = fX*fS1 + fY*fC1

	pSH[35] = SH_5_COEF * fC0
	pSH[25] = SH_5_COEF * fS0
}
