package hrtf

import (
	_ "embed"
	"sync"
)

// The shipped default filter set: TH Köln KU100 (Bernschütz 2013, CC BY 3.0,
// measured at 3.25 m), ear-aligned BiMagLS design (M4), orders 2–5, built by
// ../panaudia-hrtf. The manifest's alignment block (Woodworth radius fitted
// to the set's measured ITDs) is the runtime's delay model — the filter set
// and the delay source switch together, never independently.
//
// Committed alongside but not embedded (tests read them from disk):
// sets/legacy-kemar-anchor-v1.pahrtf (the M3 runtime anchor) and
// sets/thk-ku100-magls-full-v1.pahrtf (the M3 ITD-in-filter set, kept as
// the M8 A/B pipeline-vs-voicing separator).
//
//go:embed sets/thk-ku100-bimagls-v1.pahrtf
var defaultSetBytes []byte

var (
	defaultSetOnce sync.Once
	defaultSet     *Set
)

// Default returns the embedded default filter set, parsed and validated once.
// A validation failure here is a build defect (the artifact is committed and
// CI-tested), so it is startup-fatal by design.
func Default() *Set {
	defaultSetOnce.Do(func() {
		set, err := Load(defaultSetBytes)
		if err != nil {
			panic("hrtf: embedded default set failed validation: " + err.Error())
		}
		defaultSet = set
	})
	return defaultSet
}
