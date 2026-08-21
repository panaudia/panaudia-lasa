package ambisonic

import (
	"math"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

// CombFilter represents a feedback comb filter with damping
type CombFilter struct {
	buffer      []float32
	bufferSize  int
	index       int
	feedback    float32
	damping     float32
	filterState float32 // One-pole lowpass filter state
}

// AllpassFilter represents an allpass filter for diffusion
type AllpassFilter struct {
	buffer     []float32
	bufferSize int
	index      int
	feedback   float32 // Typically 0.5 for allpass
}

type SimpleReverb struct {
	frameSize  int
	channels   int
	sampleRate float32

	// Reverb components
	combFilters    [4]*CombFilter
	allpassFilters [2]*AllpassFilter

	// Working buffer for W channel processing
	workBuffer []float32

	// Parameters
	roomSize float32 // 0.0 - 1.0, controls reverb time
	damping  float32 // 0.0 - 1.0, controls high frequency absorption
	wetMix   float32 // 0.0 - 1.0, wet/dry balance
	width    float32 // 0.0 - 1.0, stereo width (for future enhancement)

	// Smoothing for parameter changes (to avoid clicks)
	targetRoomSize float32
	targetDamping  float32
	targetWetMix   float32

	// decorr is the bilateral bus-R wet decorrelator (nil flag-off/mono).
	decorr *velvetDecorrelator
	// flushed marks that the tail has been zeroed after decaying below the
	// denormal guard threshold (so a silent scene doesn't re-clear every
	// frame).
	flushed bool
}

// Comb filter delay times in samples at 48kHz (prime numbers to avoid resonances)
var combDelayLengths = [4]int{1557, 1617, 1491, 1422}

// Allpass filter delay times in samples at 48kHz
var allpassDelayLengths = [2]int{225, 341}

// ScaleDelayLength scales a delay length based on sample rate
func scaleDelayLength(baseLength int, baseSampleRate, targetSampleRate float32) int {
	return int(float32(baseLength) * targetSampleRate / baseSampleRate)
}

func NewSimpleReverb(frameSize int, channels int, sampleRate float32) *SimpleReverb {
	reverb := &SimpleReverb{
		frameSize:  frameSize,
		channels:   channels,
		sampleRate: sampleRate,

		// Default parameter values
		roomSize: 0.5,
		damping:  0.5,
		wetMix:   0.3,
		width:    1.0,

		// Initialize targets to same values
		targetRoomSize: 0.5,
		targetDamping:  0.5,
		targetWetMix:   0.3,

		workBuffer: make([]float32, frameSize),
	}

	// Initialize comb filters
	baseSampleRate := float32(48000.0)
	for i := 0; i < 4; i++ {
		size := scaleDelayLength(combDelayLengths[i], baseSampleRate, sampleRate)
		reverb.combFilters[i] = &CombFilter{
			buffer:      make([]float32, size),
			bufferSize:  size,
			index:       0,
			feedback:    0.84, // Will be set by roomSize
			damping:     0.5,
			filterState: 0.0,
		}
	}

	// Initialize allpass filters
	for i := 0; i < 2; i++ {
		size := scaleDelayLength(allpassDelayLengths[i], baseSampleRate, sampleRate)
		reverb.allpassFilters[i] = &AllpassFilter{
			buffer:     make([]float32, size),
			bufferSize: size,
			index:      0,
			feedback:   0.5, // Standard allpass coefficient
		}
	}

	// Apply initial parameters
	reverb.updateInternalParameters()

	return reverb
}

// Parameter setters with validation

func (r *SimpleReverb) SetRoomSize(size float32) {
	r.targetRoomSize = clamp(size, 0.0, 0.999)
}

func (r *SimpleReverb) SetDamping(damp float32) {
	r.targetDamping = clamp(damp, 0.0, 1.0)
}

func (r *SimpleReverb) SetWetMix(wet float32) {
	r.targetWetMix = clamp(wet, 0.0, 1.0)
}

func (r *SimpleReverb) SetWidth(width float32) {
	r.width = clamp(width, 0.0, 1.0)
}

// Preset functions

func (r *SimpleReverb) SetPresetSmallRoom() {
	r.SetRoomSize(0.3)
	r.SetDamping(0.6)
	r.SetWetMix(0.2)
}

func (r *SimpleReverb) SetPresetMediumRoom() {
	r.SetRoomSize(0.5)
	r.SetDamping(0.5)
	r.SetWetMix(0.3)
}

func (r *SimpleReverb) SetPresetLargeHall() {
	r.SetRoomSize(0.85)
	r.SetDamping(0.3)
	r.SetWetMix(0.4)
}

func (r *SimpleReverb) SetPresetCathedral() {
	r.SetRoomSize(0.92)
	r.SetDamping(0.2)
	r.SetWetMix(0.5)
}

func (r *SimpleReverb) SetPresetTightRoom() {

	r.SetRoomSize(0.25)
	r.SetDamping(0.8)
	r.SetWetMix(0.15)
}

// Internal parameter update
func (r *SimpleReverb) updateInternalParameters() {
	// Smooth parameter interpolation (to avoid clicks)
	smoothingCoeff := float32(0.1) // Adjust for faster/slower smoothing
	r.roomSize += (r.targetRoomSize - r.roomSize) * smoothingCoeff
	r.damping += (r.targetDamping - r.damping) * smoothingCoeff
	r.wetMix += (r.targetWetMix - r.wetMix) * smoothingCoeff

	// Convert roomSize to feedback coefficient
	// roomSize 0.0 -> feedback ~0.7, roomSize 1.0 -> feedback ~0.98
	feedback := 0.7 + r.roomSize*0.28

	// Update comb filter parameters
	// Update comb filter parameters
	for i := 0; i < 4; i++ {
		r.combFilters[i].feedback = feedback

		// Link damping to both user setting AND comb filter size
		// Longer delays (larger rooms) = more damping
		delayRatio := float32(r.combFilters[i].bufferSize) / float32(combDelayLengths[0])
		r.combFilters[i].damping = r.damping * (0.5 + delayRatio*0.5)
	}
}

// Comb filter processing
func (cf *CombFilter) process(input float32) float32 {
	// Read from delay line
	output := cf.buffer[cf.index]

	// One-pole lowpass filter for damping
	// filterState = filterState * (1-damping) + output * damping
	cf.filterState = cf.filterState*(1.0-cf.damping) + output*cf.damping

	// Write to delay line with feedback
	cf.buffer[cf.index] = input + cf.filterState*cf.feedback

	// Advance index with wrap
	cf.index++
	if cf.index >= cf.bufferSize {
		cf.index = 0
	}

	return output
}

// Allpass filter processing
func (af *AllpassFilter) process(input float32) float32 {
	// Read from delay line
	bufOut := af.buffer[af.index]

	// Allpass calculation: output = -input + bufOut + feedback * input
	output := -input + bufOut

	// Write to buffer: input + feedback * bufOut
	af.buffer[af.index] = input + af.feedback*bufOut

	// Advance index with wrap
	af.index++
	if af.index >= af.bufferSize {
		af.index = 0
	}

	return output
}

// Main processing function
func (r *SimpleReverb) Apply(reverbPremix []float32, out []float32) {
	r.processWet(reverbPremix)
	r.mixInto(reverbPremix, out)
}

// processWet runs the stateful comb/allpass processing of the premix W
// channel into workBuffer. The filters advance state, so call it exactly
// once per frame; mixInto may then add the result into any number of buses
// (bilateral PostMix uses one processWet + two mixInto).
func (r *SimpleReverb) processWet(reverbPremix []float32) {
	// Update parameters smoothly
	r.updateInternalParameters()

	// Extract W channel (first frameSize samples)
	// Copy to work buffer for processing
	copy(r.workBuffer, reverbPremix[0:r.frameSize])

	// Process each sample through the reverb
	var peak float32
	for i := 0; i < r.frameSize; i++ {
		input := r.workBuffer[i]

		// Process through 4 parallel comb filters and sum
		combOut := float32(0.0)
		for j := 0; j < 4; j++ {
			combOut += r.combFilters[j].process(input)
		}
		combOut *= 0.25 // Average the 4 comb outputs

		// Process through 2 series allpass filters for diffusion
		allpassOut := r.allpassFilters[0].process(combOut)
		allpassOut = r.allpassFilters[1].process(allpassOut)

		// Store wet signal back in work buffer
		r.workBuffer[i] = allpassOut

		if input > peak {
			peak = input
		} else if -input > peak {
			peak = -input
		}
		if allpassOut > peak {
			peak = allpassOut
		} else if -allpassOut > peak {
			peak = -allpassOut
		}
	}

	// Denormal guard (plan design decision 3): the combs run in pure Go
	// (no FTZ/DAZ) and a decaying tail in a silent scene would grind
	// through denormal arithmetic indefinitely. Once the frame is fully
	// below the threshold, zero the tail state — once.
	if peak < 1e-18 {
		if !r.flushed {
			r.Clear()
			r.flushed = true
		}
	} else {
		r.flushed = false
	}

	if r.decorr != nil {
		r.runDecorrelator()
	}
}

// mixInto adds the dry premix + processed wet signal into ch 0–3 of out.
// Stateless with respect to the filters — safe to call once per bus after
// a single processWet.
func (r *SimpleReverb) mixInto(reverbPremix []float32, out []float32) {
	r.mixIntoWet(reverbPremix, r.workBuffer, out)
}

// mixIntoWet is the shared wet/dry fold: mixInto passes the processed wet
// frame (bus L), mixIntoDecorrelated the velvet-decorrelated one (bus R).
// The gains and per-channel shares live only here.
func (r *SimpleReverb) mixIntoWet(reverbPremix, wet, out []float32) {
	dryGain := 1.0 - r.wetMix*0.5 // Reduce dry slightly when wet is high
	wetGain := r.wetMix

	var share float32 = 0.4

	for ch := 0; ch < common.REVERB_CHANNELS; ch++ {
		srcOffset := ch * r.frameSize
		for i := 0; i < r.frameSize; i++ {
			out[srcOffset+i] = out[srcOffset+i] + (reverbPremix[srcOffset+i]*dryGain+wet[i]*wetGain)*share
		}
		share = 0.2
	}
}

// --- Bilateral wet decorrelation (M4, plan design decision 7) -----------
//
// With ear-aligned decode filters the ITD phase that used to decorrelate
// the late reverb between ears is gone — identical wet in both buses
// collapses the reverb image to the interaural midline. Bus R's wet signal
// is therefore passed through a fixed velvet-noise sparse FIR (~20 ms, 18
// signed taps, energy-normalized): cheap (18 MACs/sample), unconditionally
// stable, spectrally near-flat, and deterministic. Only the WET signal is
// decorrelated — the dry premix carries per-source spatial weights and
// must stay identical across buses.

// velvetTapPositions/Signs: 18 taps jitter-scattered over 960 samples
// (20 ms @ 48 kHz), fixed so every build renders identically.
var velvetTapPositions = [18]int{
	5, 67, 131, 172, 241, 290, 353, 419, 460,
	529, 587, 631, 701, 748, 811, 866, 917, 953,
}
var velvetTapSigns = [18]float32{
	1, -1, -1, 1, -1, 1, 1, -1, 1, -1, 1, 1, -1, -1, 1, -1, -1, 1,
}

const velvetLength = 960

type velvetDecorrelator struct {
	// hist keeps the last velvetLength wet samples ahead of the current
	// frame: [velvetLength history | frameSize current].
	hist []float32
	out  []float32
	gain float32
}

// EnableDecorrelator switches on bus-R wet decorrelation (bilateral only;
// called at encoder construction, never on the audio thread).
func (r *SimpleReverb) EnableDecorrelator() {
	r.decorr = &velvetDecorrelator{
		hist: make([]float32, velvetLength+r.frameSize),
		out:  make([]float32, r.frameSize),
		gain: float32(1.0 / math.Sqrt(float64(len(velvetTapPositions)))),
	}
}

// runDecorrelator pushes the just-processed wet frame into the history and
// computes the decorrelated frame. Called from processWet so the history
// advances exactly once per frame.
func (r *SimpleReverb) runDecorrelator() {
	d := r.decorr
	copy(d.hist[:velvetLength], d.hist[r.frameSize:r.frameSize+velvetLength])
	copy(d.hist[velvetLength:], r.workBuffer[:r.frameSize])
	for n := 0; n < r.frameSize; n++ {
		var acc float32
		for k, p := range velvetTapPositions {
			acc += velvetTapSigns[k] * d.hist[velvetLength+n-p]
		}
		d.out[n] = acc * d.gain
	}
}

// mixIntoDecorrelated is mixInto with the decorrelated wet signal (bus R).
func (r *SimpleReverb) mixIntoDecorrelated(reverbPremix []float32, out []float32) {
	if r.decorr == nil {
		r.mixInto(reverbPremix, out)
		return
	}
	r.mixIntoWet(reverbPremix, r.decorr.out, out)
}

// Utility function
func clamp(value, min, max float32) float32 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// Optional: Get current parameter values
func (r *SimpleReverb) GetRoomSize() float32 {
	return r.roomSize
}

func (r *SimpleReverb) GetDamping() float32 {
	return r.damping
}

func (r *SimpleReverb) GetWetMix() float32 {
	return r.wetMix
}

// Optional: Clear reverb tail (useful for scene changes)
func (r *SimpleReverb) Clear() {
	for i := 0; i < 4; i++ {
		for j := range r.combFilters[i].buffer {
			r.combFilters[i].buffer[j] = 0
		}
		r.combFilters[i].filterState = 0
	}

	for i := 0; i < 2; i++ {
		for j := range r.allpassFilters[i].buffer {
			r.allpassFilters[i].buffer[j] = 0
		}
	}

	for i := range r.workBuffer {
		r.workBuffer[i] = 0
	}
}
