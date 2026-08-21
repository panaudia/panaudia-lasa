package ambisonic

import "github.com/panaudia/panaudia-lasa/engine/common"

// Listener-position split (engine-added; PROVENANCE.md divergence #2).
//
// PositionMeters historically served two roles: this node's position as a
// SOURCE (read by peers' geometry passes) and as a LISTENER (its own
// mix's reference point). The engine feeds the two roles from different
// pose pipelines — source position jitter-aligned with the node's own
// audio, listener position fresh from the sink's latest pose — so the
// listener role gets its own field. Encoders that never call
// SetListenerPosition behave exactly as before (fall back to
// PositionMeters), which keeps the copied tests byte-for-byte valid.

// SetListenerPosition sets the position used when this encoder renders
// its own mix. Same input domain as SetPosition (scaled by
// mixerConfig.Size once, here).
func (encoder *Encoder) SetListenerPosition(position common.Position) {
	encoder.listenerPosMeters = common.Position{
		X: position.X * encoder.mixerConfig.Size,
		Y: position.Y * encoder.mixerConfig.Size,
		Z: position.Z * encoder.mixerConfig.Size,
	}
	encoder.hasListenerPos = true
}

// ListenerPositionMeters is the listener-role position: the value set by
// SetListenerPosition, or PositionMeters if it was never called.
func (encoder *Encoder) ListenerPositionMeters() common.Position {
	if encoder.hasListenerPos {
		return encoder.listenerPosMeters
	}
	return encoder.PositionMeters
}
