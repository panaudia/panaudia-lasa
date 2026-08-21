package ambisonic

import (
	"os"
	"testing"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// NewEncoder is always dual-bus since M9.4 and panics without the Woodworth
// delay model (production wires it from the HRTF manifest). Install the
// standard test parameters for the whole package; tests that need specific
// values still use setTestDelayModel (which restores on cleanup).
func TestMain(m *testing.M) {
	common.BilateralHeadRadiusM = 0.0875
	common.BilateralSpeedOfSoundMS = 343.0
	os.Exit(m.Run())
}
