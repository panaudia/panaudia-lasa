package inout

// Sink encoder bitrates (bits per second), pinned here so a change is a
// decision rather than an inherited libopus default.
//
// Uplink mono objects arrive at 64 kb/s by default (bridge and browser
// client). Listening work found 48 kb/s per channel sufficient to
// preserve binaural spatial imaging, hence 96 kb/s for the stereo pair.
// The ambisonic figure is per uncoupled mono stream, close to what
// libopus chose unpinned (its 5 ms mono default measured ~69 kb/s on
// noise; the pin measures ~62). It is uniform across components, and a
// per-order taper (higher components carry less energy) is an open
// listening question, not yet a decision.
//
// Per listener, measured on noise at 5 ms frames: binaural ≈ 98 kb/s,
// ambi2 ≈ 560 kb/s, ambi3 ≈ 1.0 Mb/s.
const (
	BinauralBitrate   = 96000
	AmbiStreamBitrate = 60000
)
