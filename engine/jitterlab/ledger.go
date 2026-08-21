package jitterlab

// ledger.go — the black-box decoder and artifact ledger. The decoder is a
// small state machine fed each read's output block; it reconstructs the
// played stream positions from the ramp signal and emits typed artifacts.
// The buffer under test is never asked what it did — everything is
// measured from its output (plan/jitter-v4-harness.md, "black-box
// measurement").
//
// Marker (splice) classification is by value, on sight: relative to the
// last played position p, the buffer's single-tap average produces
// exactly avg(ramp(p), ramp(p+1)) for an INSERT (the marker previews the
// next frame without consuming it) and exactly avg(ramp(p+1), ramp(p+2))
// for a DROP (the marker averages two consumed frames). Both are
// bit-exact float32 comparisons. Classifying on sight — rather than from
// the span to the next position — survives a splice-read being followed
// immediately by a snap-read, which destroys span evidence (found by the
// phase-1 cross-check against buffer stats).
//
// Silence before the first decoded position is join/startup latency, not
// an artifact; silence after it is an underrun run.

// ArtifactKind labels a ledger entry.
type ArtifactKind uint8

const (
	// ArtifactDrop is a ±1 drop splice (one frame removed).
	ArtifactDrop ArtifactKind = iota
	// ArtifactInsert is a ±1 insert splice (one frame synthesized).
	ArtifactInsert
	// ArtifactSnap is a discontinuous stream-position jump (startup
	// placement excluded — the first position seen is the baseline).
	ArtifactSnap
	// ArtifactSilence is a run of silent output after first audio (an
	// underrun run). Delta holds the run length in frames.
	ArtifactSilence
	// ArtifactAlien is a sample the ramp codec cannot explain. Zero in
	// any healthy run against the v3 buffer, except loss scenarios,
	// where a splice landing exactly on a loss gap averages positions
	// the codec can't pair (bounded by the splice count at gaps).
	ArtifactAlien
	// ArtifactLossGap is a forward stream jump fully explained by
	// upstream packet loss (positions the writer never delivered).
	// Assigned by the runner's gap ledger, never by the decoder — the
	// decoder initially reports these as snaps.
	ArtifactLossGap
)

func (k ArtifactKind) String() string {
	switch k {
	case ArtifactDrop:
		return "drop"
	case ArtifactInsert:
		return "insert"
	case ArtifactSnap:
		return "snap"
	case ArtifactSilence:
		return "silence"
	case ArtifactAlien:
		return "alien"
	case ArtifactLossGap:
		return "lossgap"
	}
	return "?"
}

// Artifact is one ledger entry.
type Artifact struct {
	Kind ArtifactKind
	Time int64 // master-clock frame time of the read that surfaced it
	Pos  int64 // absolute stream position at/after the event
	// Delta: for Snap, the signed position jump relative to contiguous
	// playback; for Silence, the run length in frames; else 0.
	Delta int64
}

// decoder consumes read output blocks and appends artifacts.
type decoder struct {
	nc int

	lastPos int64 // absolute position of last played frame; -1 until first

	silenceOpen   bool
	silenceTime   int64
	silenceFrames int64

	joinSilenceFrames int64 // silence before first audio (not an artifact)

	artifacts []Artifact

	drops, inserts, snaps, silenceRuns int64
	alienSamples                       int64
}

func newDecoder(nc int) *decoder {
	return &decoder{nc: nc, lastPos: -1}
}

// feed decodes one read block (interleaved; channel 0 is decoded — splices
// move whole frames). Returns the absolute stream position of the block's
// first plain sample, or -1 if the block contained none.
func (d *decoder) feed(time int64, block []float32) int64 {
	firstPos := int64(-1)
	nFrames := int64(len(block)) / int64(d.nc)
	for f := int64(0); f < nFrames; f++ {
		kind, w := classifySample(block[f*int64(d.nc)])
		switch kind {
		case sampSilence:
			if d.lastPos < 0 {
				d.joinSilenceFrames++
				continue
			}
			if !d.silenceOpen {
				d.silenceOpen = true
				d.silenceTime = time
				d.silenceFrames = 0
			}
			d.silenceFrames++
		case sampPlain:
			d.closeSilence()
			pos := d.plainSample(time, w)
			if firstPos < 0 {
				firstPos = pos
			}
		case sampMarker:
			d.closeSilence()
			d.markerSample(time, block[f*int64(d.nc)])
		case sampAlien:
			d.alienSamples++
		}
	}
	return firstPos
}

// plainSample handles one decoded position: detects snaps against the
// expected next frame, then advances lastPos. Returns the absolute
// position.
func (d *decoder) plainSample(time, w int64) int64 {
	if d.lastPos < 0 {
		// First audio ever: baseline, not an artifact.
		d.lastPos = w
		return w
	}
	pos := unwrapPos(d.lastPos, w)
	if pos != d.lastPos+1 {
		d.snaps++
		d.artifacts = append(d.artifacts, Artifact{ArtifactSnap, time, pos, pos - (d.lastPos + 1)})
	}
	d.lastPos = pos
	return pos
}

// markerSample classifies a splice marker by value against the expected
// insert/drop encodings for the current position. An insert plays a
// synthetic preview of lastPos+1 without consuming it (lastPos
// unchanged); a drop plays through lastPos+2 (lastPos advances), so the
// snap rule keeps working across the marker in both cases.
func (d *decoder) markerSample(time int64, v float32) {
	if d.lastPos < 0 {
		d.alienSamples++ // marker before any position: not producible
		return
	}
	switch v {
	case markerValue(d.lastPos):
		d.inserts++
		d.artifacts = append(d.artifacts, Artifact{ArtifactInsert, time, d.lastPos + 1, 0})
	case markerValue(d.lastPos + 1):
		d.drops++
		d.artifacts = append(d.artifacts, Artifact{ArtifactDrop, time, d.lastPos + 2, 0})
		d.lastPos += 2
	default:
		d.alienSamples++
	}
}

func (d *decoder) closeSilence() {
	if !d.silenceOpen {
		return
	}
	d.silenceOpen = false
	d.silenceRuns++
	d.artifacts = append(d.artifacts, Artifact{ArtifactSilence, d.silenceTime, d.lastPos, d.silenceFrames})
}

// finish closes any open state at end of run.
func (d *decoder) finish() {
	d.closeSilence()
}
