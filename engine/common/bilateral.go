package common

import (
	"os"
)

// Bilateral rendering is the only binaural path since M9.4
// (plan/m9-saf-exit/plan.md): the PANAUDIA_BILATERAL flag and the legacy
// single-mix ambi_bin decode are gone. Render mode is per-output — binaural
// outputs are dual-bus ear-centered, raw ambisonic outputs (ROC/Link) are
// the permanent classic world-frame encode (NewClassicEncoder, design
// decision 10). Rollback is the previous image tag, not a flag.
func init() {
	if os.Getenv("PANAUDIA_BILATERAL") != "" {
		LogWarn("PANAUDIA_BILATERAL is deprecated and ignored — bilateral rendering is always on (raw outputs render classic per output; see plan/m9-saf-exit/plan.md)")
	}
}

// DelayRingHistory is the per-source input-ring history the bilateral
// encoder keeps ahead of the current frame (ambisonic.DelayRingHistory
// aliases it). It lives here so the PAHRTF loader can reject any set whose
// Woodworth model delays (delayBase + max tau/2, from head_radius_m and
// speed_of_sound_m_s) would not fit — load-time rejection instead of a
// connect-time panic in initDelayModel. The two checks share this constant
// and the same closed form, so they cannot drift.
const DelayRingHistory = 64

// Woodworth delay model for encode-side ITD (M4, plan design decision 2).
// Set once at startup from the loaded HRTF set's manifest — NEVER from
// independent config: the offline tool removed exactly these model delays
// during ear-alignment, so the filter set and the delay parameters must
// switch together (the alignment-consistency invariant). Zero radius means
// "not configured"; encoder construction panics on it when Bilateral is on.
var (
	BilateralHeadRadiusM    float64
	BilateralSpeedOfSoundMS float64
)

// Per-ear parallax geometry (M5). BilateralEarOffsetM is the GEOMETRIC ear
// offset from the head centre on the interaural axis — deliberately not the
// Woodworth radius above, which is an ITD-estimator fit (larger than the
// physical head). BilateralMeasurementRadiusM is the HRTF set's measurement
// distance (from the manifest at startup); per-ear rays are projected onto
// that sphere for the SH lookup. 0 = unknown → the projection degrades to
// the plain from-the-ear direction (the radius→∞ limit).
var (
	BilateralEarOffsetM         = 0.0875
	BilateralMeasurementRadiusM float64
)
