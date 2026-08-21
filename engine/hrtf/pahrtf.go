// Package hrtf loads PAHRTF filter-set containers produced by the offline
// tool in ../panaudia-hrtf. The container is the only contract between the
// tool and the runtime: the tool is the only thing that ever parses SOFA,
// the runtime parses only this format. The default set ships embedded; the
// same loader serves runtime-loaded custom sets later.
//
// Layout (little-endian):
//
//	bytes 0..7   magic "PAHRTF\x00\x00"
//	u32          format version (1)
//	u32          header length in bytes
//	header       JSON manifest (UTF-8)
//	blocks       raw float32 LE arrays, 64-byte aligned; the manifest's
//	             "blocks" list records id/shape/dtype/offset
//
// Validations here mirror panaudia_hrtf/pahrtf.py read_pahrtf — keep them
// in sync.
package hrtf

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/panaudia/panaudia-lasa/engine/common"
)

const (
	Version       = 1
	SampleRate    = 48000
	MaxFilterTaps = 273 // runtime convolver cap (FFT-512, 240-sample block)

	// maxBulkDelaySamples bounds the set's decode latency: 10 ms. The whole
	// point of the convolver path is recovering ambi_bin's 30 ms; a set
	// claiming more delay than this is malformed, not just slow.
	maxBulkDelaySamples = 480

	// maxITDSeconds is a sanity bound on the ITD table: SAF clamps
	// estimated ITDs to ±sqrt(2)/2000 s ≈ ±0.71 ms; 2 ms catches
	// unit mistakes (samples vs seconds) without being brittle.
	maxITDSeconds = 2e-3
)

var magic = []byte("PAHRTF\x00\x00")

// Filter types the container may declare. MagLS-full keeps ITD inside the
// filters (alignment method must be "none"); ear-aligned sets (M4) carry
// their ITD in a delay model and must name it, so the runtime can refuse a
// set/delay-model mismatch at load time (the alignment-consistency
// invariant, plan decision 2).
const (
	FilterTypeMagLSFull  = "magls-full"
	FilterTypeEarAligned = "bimagls-ear-aligned"
)

type BlockEntry struct {
	ID     string `json:"id"`
	Shape  []int  `json:"shape"`
	Dtype  string `json:"dtype"`
	Offset int    `json:"offset"`
}

type Alignment struct {
	Method      string  `json:"method"`
	HeadRadiusM float64 `json:"head_radius_m,omitempty"`
	// SpeedOfSoundMS pins the c used by the offline alignment so the
	// runtime's delay reinsertion evaluates the identical closed form.
	SpeedOfSoundMS float64 `json:"speed_of_sound_m_s,omitempty"`
	// T0RemovedS is the constant time-of-flight the tool stripped from the
	// measured phase (provenance only; it carries no spatial information).
	T0RemovedS float64 `json:"t0_removed_s,omitempty"`
}

type Manifest struct {
	FormatVersion    int            `json:"format_version"`
	Name             string         `json:"name"`
	SampleRate       int            `json:"sample_rate"`
	FilterType       string         `json:"filter_type"`
	FilterLen        int            `json:"filter_len"`
	BulkDelaySamples int            `json:"bulk_delay_samples"`
	Orders           []int          `json:"orders"`
	Alignment        Alignment      `json:"alignment"`
	Source           map[string]any `json:"source"`
	Blocks           []BlockEntry   `json:"blocks"`
}

// FilterBank is one order's decode filters: taps laid out ear-major,
// [2][numChannels][FilterLen] contiguous — ear 0 = left, channels in ACN
// order — so the convolver can take one base pointer per ear.
type FilterBank struct {
	Order       int
	NumChannels int // (Order+1)^2
	FilterLen   int
	Taps        []float32
}

// EarTaps returns the FilterLen taps for one ear (0=left, 1=right) and one
// ambisonic channel (ACN index).
func (b *FilterBank) EarTaps(ear, ch int) []float32 {
	off := (ear*b.NumChannels + ch) * b.FilterLen
	return b.Taps[off : off+b.FilterLen]
}

// Set is a parsed, validated PAHRTF container.
type Set struct {
	Manifest Manifest

	// MeasurementRadiusM is the HRIR measurement distance from the source
	// provenance (0 if the set doesn't record one). The parallax projection
	// (M5) projects per-ear rays onto this sphere.
	MeasurementRadiusM float64

	// ITD lookup table on the measurement grid (M4 consumes this for
	// ear-aligned encode delays; carried by every set for provenance).
	ITDDirsDeg [][2]float32
	ITDDelaysS []float32

	banks map[int]*FilterBank
}

// Bank returns the filter bank for the given ambisonic order, or nil if the
// set does not carry that order.
func (s *Set) Bank(order int) *FilterBank {
	return s.banks[order]
}

// LoadFile reads and validates a PAHRTF container from a local path.
// (Future custom-set delivery fetches from a URL first; the bytes then come
// through Load — this loader never assumes local paths beyond this helper.)
func LoadFile(path string) (*Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hrtf: %w", err)
	}
	return Load(data)
}

// Load parses and validates a PAHRTF container. Every validation failure is
// an error, never a partial set: a bad file must be rejected whole (a NaN in
// a filter table would propagate into every mix).
func Load(data []byte) (*Set, error) {
	if len(data) < len(magic)+8 {
		return nil, fmt.Errorf("hrtf: file too short (%d bytes)", len(data))
	}
	if !bytes.Equal(data[:len(magic)], magic) {
		return nil, fmt.Errorf("hrtf: not a PAHRTF file (bad magic)")
	}
	version := binary.LittleEndian.Uint32(data[len(magic):])
	if version != Version {
		return nil, fmt.Errorf("hrtf: unsupported PAHRTF version %d", version)
	}
	headerLen := int(binary.LittleEndian.Uint32(data[len(magic)+4:]))
	headerStart := len(magic) + 8
	if headerLen <= 0 || headerStart+headerLen > len(data) {
		return nil, fmt.Errorf("hrtf: header length %d exceeds file size %d", headerLen, len(data))
	}

	var m Manifest
	if err := json.Unmarshal(data[headerStart:headerStart+headerLen], &m); err != nil {
		return nil, fmt.Errorf("hrtf: manifest: %w", err)
	}
	if m.FormatVersion != Version {
		return nil, fmt.Errorf("hrtf: manifest format_version %d != container version %d", m.FormatVersion, Version)
	}
	if m.SampleRate != SampleRate {
		return nil, fmt.Errorf("hrtf: sample_rate must be %d, got %d", SampleRate, m.SampleRate)
	}
	if m.FilterLen <= 0 || m.FilterLen > MaxFilterTaps {
		return nil, fmt.Errorf("hrtf: filter_len %d outside (0, %d]", m.FilterLen, MaxFilterTaps)
	}
	if m.BulkDelaySamples < 0 || m.BulkDelaySamples > maxBulkDelaySamples {
		return nil, fmt.Errorf("hrtf: bulk_delay_samples %d outside [0, %d]", m.BulkDelaySamples, maxBulkDelaySamples)
	}
	if err := checkAlignmentConsistency(m); err != nil {
		return nil, err
	}
	if len(m.Orders) == 0 {
		return nil, fmt.Errorf("hrtf: manifest lists no orders")
	}

	blocks := make(map[string]BlockEntry, len(m.Blocks))
	for _, e := range m.Blocks {
		if e.Dtype != "f32le" {
			return nil, fmt.Errorf("hrtf: block %q: unsupported dtype %q", e.ID, e.Dtype)
		}
		if _, dup := blocks[e.ID]; dup {
			return nil, fmt.Errorf("hrtf: duplicate block %q", e.ID)
		}
		count := 1
		for _, d := range e.Shape {
			if d <= 0 {
				return nil, fmt.Errorf("hrtf: block %q: non-positive dimension %d", e.ID, d)
			}
			if count > len(data)/d { // overflow-safe count*d <= len(data)
				return nil, fmt.Errorf("hrtf: block %q: shape %v exceeds file size", e.ID, e.Shape)
			}
			count *= d
		}
		if e.Offset < headerStart+headerLen || e.Offset%64 != 0 {
			return nil, fmt.Errorf("hrtf: block %q: bad offset %d (must be 64-aligned, past header)", e.ID, e.Offset)
		}
		if e.Offset+count*4 > len(data) {
			return nil, fmt.Errorf("hrtf: block %q: extends past end of file", e.ID)
		}
		blocks[e.ID] = e
	}

	set := &Set{Manifest: m, banks: make(map[int]*FilterBank, len(m.Orders))}
	if r, ok := m.Source["measurement_radius_m"].(float64); ok && r > 0 {
		set.MeasurementRadiusM = r
	}

	for _, order := range m.Orders {
		if order < 1 || order > 7 {
			return nil, fmt.Errorf("hrtf: order %d outside supported range [1, 7]", order)
		}
		id := fmt.Sprintf("filters_order%d", order)
		e, ok := blocks[id]
		if !ok {
			return nil, fmt.Errorf("hrtf: manifest lists order %d but block %q is missing", order, id)
		}
		nCh := (order + 1) * (order + 1)
		if len(e.Shape) != 3 || e.Shape[0] != 2 || e.Shape[1] != nCh || e.Shape[2] != m.FilterLen {
			return nil, fmt.Errorf("hrtf: block %q: shape %v, want [2 %d %d]", id, e.Shape, nCh, m.FilterLen)
		}
		taps, err := readFloats(data, e)
		if err != nil {
			return nil, err
		}
		set.banks[order] = &FilterBank{
			Order:       order,
			NumChannels: nCh,
			FilterLen:   m.FilterLen,
			Taps:        taps,
		}
	}

	dirsEntry, ok := blocks["itd_dirs_deg"]
	if !ok {
		return nil, fmt.Errorf("hrtf: missing block %q", "itd_dirs_deg")
	}
	delaysEntry, ok := blocks["itd_delays_s"]
	if !ok {
		return nil, fmt.Errorf("hrtf: missing block %q", "itd_delays_s")
	}
	if len(dirsEntry.Shape) != 2 || dirsEntry.Shape[1] != 2 {
		return nil, fmt.Errorf("hrtf: block itd_dirs_deg: shape %v, want [N 2]", dirsEntry.Shape)
	}
	if len(delaysEntry.Shape) != 1 || delaysEntry.Shape[0] != dirsEntry.Shape[0] {
		return nil, fmt.Errorf("hrtf: ITD table mismatch: %d directions, %v delays", dirsEntry.Shape[0], delaysEntry.Shape)
	}
	dirsFlat, err := readFloats(data, dirsEntry)
	if err != nil {
		return nil, err
	}
	delays, err := readFloats(data, delaysEntry)
	if err != nil {
		return nil, err
	}
	for _, d := range delays {
		if d < -maxITDSeconds || d > maxITDSeconds {
			return nil, fmt.Errorf("hrtf: ITD %.3g s outside sanity bound ±%.0e s (samples-vs-seconds mix-up?)", d, maxITDSeconds)
		}
	}
	set.ITDDirsDeg = make([][2]float32, dirsEntry.Shape[0])
	for i := range set.ITDDirsDeg {
		set.ITDDirsDeg[i] = [2]float32{dirsFlat[2*i], dirsFlat[2*i+1]}
	}
	set.ITDDelaysS = delays

	return set, nil
}

// InstallDelayModel publishes the set's alignment parameters into the
// common.Bilateral* globals the encode-side Woodworth delay model (M4) and
// per-ear parallax (M5) read — the mechanical form of the
// alignment-consistency invariant: the delay/geometry parameters and the
// filter set switch together, never independently. Every deployment wires
// them through this one helper. Encoders must be constructed AFTER this
// runs (flag-on construction panics on unset parameters).
//
// The returned restore func puts the previous values back — production
// callers ignore it; tests defer it.
func (set *Set) InstallDelayModel() (restore func()) {
	prevA := common.BilateralHeadRadiusM
	prevC := common.BilateralSpeedOfSoundMS
	prevR := common.BilateralMeasurementRadiusM
	common.BilateralHeadRadiusM = set.Manifest.Alignment.HeadRadiusM
	common.BilateralSpeedOfSoundMS = set.Manifest.Alignment.SpeedOfSoundMS
	common.BilateralMeasurementRadiusM = set.MeasurementRadiusM
	return func() {
		common.BilateralHeadRadiusM = prevA
		common.BilateralSpeedOfSoundMS = prevC
		common.BilateralMeasurementRadiusM = prevR
	}
}

// checkAlignmentConsistency enforces the alignment-consistency invariant
// mechanically (m3-design Part B): the filter type and the alignment method
// must switch together, never independently. MagLS-full keeps the ITD inside
// the filters, so it must declare no alignment; ear-aligned filters have had
// a delay model removed at design time and must name it, so the runtime's
// delay source can be checked against it.
func checkAlignmentConsistency(m Manifest) error {
	switch m.FilterType {
	case FilterTypeMagLSFull:
		if m.Alignment.Method != "none" {
			return fmt.Errorf("hrtf: %s set declares alignment method %q, must be \"none\" (ITD lives in the filters)",
				FilterTypeMagLSFull, m.Alignment.Method)
		}
	case FilterTypeEarAligned:
		switch m.Alignment.Method {
		case "woodworth", "measured":
			if m.Alignment.HeadRadiusM <= 0 {
				return fmt.Errorf("hrtf: %s set with method %q must carry head_radius_m",
					FilterTypeEarAligned, m.Alignment.Method)
			}
			if m.Alignment.SpeedOfSoundMS <= 0 {
				return fmt.Errorf("hrtf: %s set with method %q must carry speed_of_sound_m_s",
					FilterTypeEarAligned, m.Alignment.Method)
			}
			// The runtime reinserts this set's Woodworth delays from a
			// fixed-size per-source ring; reject a set whose worst-case
			// per-ear read (delayBase + max tau/2, the exact form of
			// ambisonic.initDelayModel) would not fit, so a custom set
			// fails at load instead of panicking at first connect.
			maxTau := m.Alignment.HeadRadiusM / m.Alignment.SpeedOfSoundMS *
				(math.Pi/2 + 1) * SampleRate
			delayBase := math.Ceil(maxTau/2) + 1
			if delayBase+maxTau/2 > common.DelayRingHistory {
				aMax := (common.DelayRingHistory - 2) * m.Alignment.SpeedOfSoundMS /
					((math.Pi/2 + 1) * SampleRate)
				return fmt.Errorf("hrtf: head_radius_m %.4g at %.4g m/s needs %.0f samples of delay ring, have %d (max radius ≈ %.3f m at this speed of sound)",
					m.Alignment.HeadRadiusM, m.Alignment.SpeedOfSoundMS,
					delayBase+maxTau/2, common.DelayRingHistory, aMax)
			}
		default:
			return fmt.Errorf("hrtf: %s set must declare alignment method \"woodworth\" or \"measured\", got %q",
				FilterTypeEarAligned, m.Alignment.Method)
		}
	default:
		return fmt.Errorf("hrtf: unknown filter_type %q", m.FilterType)
	}
	return nil
}

// readFloats decodes a block's float32 values, rejecting NaN/Inf anywhere.
func readFloats(data []byte, e BlockEntry) ([]float32, error) {
	count := 1
	for _, d := range e.Shape {
		count *= d
	}
	out := make([]float32, count)
	raw := data[e.Offset : e.Offset+count*4]
	for i := range out {
		bits := binary.LittleEndian.Uint32(raw[i*4:])
		if bits&0x7F800000 == 0x7F800000 { // exponent all-ones: NaN or Inf
			return nil, fmt.Errorf("hrtf: block %q contains non-finite value at index %d", e.ID, i)
		}
		out[i] = math.Float32frombits(bits)
	}
	return out, nil
}
