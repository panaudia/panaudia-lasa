//go:build race

package engine

// raceEnabled reports whether the race detector instruments this build;
// performance gates skip themselves under it — an instrumented binary's
// timings say nothing about the real one.
const raceEnabled = true
