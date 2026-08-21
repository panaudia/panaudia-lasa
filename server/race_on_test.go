//go:build race

package main

// raceEnabled reports whether the race detector instruments this build;
// timing gates are meaningless under its ~10× slowdown (the engine
// repo's convention, mirrored).
const raceEnabled = true
