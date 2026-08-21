package hrtf

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// --- committed artifacts -------------------------------------------------

func TestDefaultSet(t *testing.T) {
	set := Default()
	m := set.Manifest
	if m.Name != "thk-ku100-bimagls-v1" {
		t.Errorf("name = %q", m.Name)
	}
	if m.FilterType != FilterTypeEarAligned || m.Alignment.Method != "woodworth" {
		t.Errorf("filter_type/alignment = %q/%q", m.FilterType, m.Alignment.Method)
	}
	// The Woodworth radius is fitted to the set's measured (750 Hz low-passed)
	// ITDs, so it exceeds the KU100's geometric radius; c is pinned so the
	// runtime reinsertion evaluates the identical closed form.
	if m.Alignment.HeadRadiusM < 0.09 || m.Alignment.HeadRadiusM > 0.14 ||
		m.Alignment.SpeedOfSoundMS != 343.0 {
		t.Errorf("alignment = %+v", m.Alignment)
	}
	if m.FilterLen != 272 || m.BulkDelaySamples != 36 {
		t.Errorf("filter_len/bulk_delay = %d/%d, want 272/36", m.FilterLen, m.BulkDelaySamples)
	}
	for _, order := range []int{2, 3, 4, 5} {
		b := set.Bank(order)
		if b == nil {
			t.Fatalf("missing bank for order %d", order)
		}
		nCh := (order + 1) * (order + 1)
		if b.NumChannels != nCh || len(b.Taps) != 2*nCh*272 {
			t.Errorf("order %d: channels %d taps %d", order, b.NumChannels, len(b.Taps))
		}
		if len(b.EarTaps(1, nCh-1)) != 272 {
			t.Errorf("order %d: EarTaps length", order)
		}
	}
	if set.Bank(1) != nil {
		t.Error("unexpected order-1 bank")
	}
	if len(set.ITDDirsDeg) != 2702 || len(set.ITDDelaysS) != 2702 {
		t.Errorf("ITD table sizes %d/%d, want 2702 (Lebedev-2702)", len(set.ITDDirsDeg), len(set.ITDDelaysS))
	}
	// The W-channel (ACN 0) filters must carry near-equal energy across ears
	// for a symmetric dummy head. (Time-domain correlation is the wrong
	// metric on a measured set: MagLS phase above cutoff differs per ear.)
	b := set.Bank(3)
	l, r := b.EarTaps(0, 0), b.EarTaps(1, 0)
	ratio := 10 * math.Log10(energy(l)/energy(r))
	if math.Abs(ratio) > 1.0 {
		t.Errorf("W-channel L/R energy ratio %.2f dB, want |ratio| <= 1 dB", ratio)
	}
}

func TestAnchorSet(t *testing.T) {
	set, err := LoadFile("sets/legacy-kemar-anchor-v1.pahrtf")
	if err != nil {
		t.Fatal(err)
	}
	// The anchor exists solely for the runtime comparison at panaudia's
	// default order: orders 2+3 only, SAF-faithful wrapped IPD.
	if got := set.Manifest.Orders; len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("orders = %v, want [2 3]", got)
	}
	if set.Bank(3) == nil || set.Bank(5) != nil {
		t.Error("anchor bank presence wrong")
	}
}

func TestMagLSFullSetStillLoads(t *testing.T) {
	// The M3 ITD-in-filter set stays committed (M8 A/B separator); it must
	// keep loading even though it is no longer the embedded default.
	set, err := LoadFile("sets/thk-ku100-magls-full-v1.pahrtf")
	if err != nil {
		t.Fatal(err)
	}
	if set.Manifest.FilterType != FilterTypeMagLSFull || set.Manifest.BulkDelaySamples != 97 {
		t.Errorf("filter_type/bulk = %q/%d", set.Manifest.FilterType, set.Manifest.BulkDelaySamples)
	}
}

func energy(x []float32) float64 {
	var e float64
	for _, v := range x {
		e += float64(v) * float64(v)
	}
	return e
}

// --- validation paths, via an independent in-test container writer -------

type testBlock struct {
	id    string
	shape []int
	data  []float32
}

// writeContainer mirrors panaudia_hrtf/pahrtf.py write_pahrtf independently
// (fixed-point iteration on the header size, 64-byte-aligned blocks).
func writeContainer(t *testing.T, manifest map[string]any, tblocks []testBlock) []byte {
	t.Helper()
	var headerBytes []byte
	offsets := make([]int, len(tblocks))
	for range 3 {
		entries := make([]map[string]any, 0, len(tblocks))
		cursor := len(magic) + 8 + len(headerBytes)
		for i, b := range tblocks {
			cursor = (cursor + 63) / 64 * 64
			offsets[i] = cursor
			entries = append(entries, map[string]any{
				"id": b.id, "shape": b.shape, "dtype": "f32le", "offset": cursor,
			})
			cursor += len(b.data) * 4
		}
		manifest["format_version"] = Version
		manifest["blocks"] = entries
		newHeader, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if len(newHeader) == len(headerBytes) {
			headerBytes = newHeader
			break
		}
		headerBytes = newHeader
	}
	out := make([]byte, 0, 4096)
	out = append(out, magic...)
	out = binary.LittleEndian.AppendUint32(out, Version)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(headerBytes)))
	out = append(out, headerBytes...)
	for i, b := range tblocks {
		for len(out) < offsets[i] {
			out = append(out, 0)
		}
		for _, v := range b.data {
			out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v))
		}
	}
	return out
}

func validManifest() map[string]any {
	return map[string]any{
		"name":               "test-set",
		"sample_rate":        48000,
		"filter_type":        FilterTypeMagLSFull,
		"filter_len":         8,
		"bulk_delay_samples": 10,
		"orders":             []int{1},
		"alignment":          map[string]any{"method": "none"},
	}
}

func validBlocks() []testBlock {
	return []testBlock{
		{"filters_order1", []int{2, 4, 8}, make([]float32, 2*4*8)},
		{"itd_dirs_deg", []int{4, 2}, make([]float32, 8)},
		{"itd_delays_s", []int{4}, []float32{1e-4, -1e-4, 0, 5e-4}},
	}
}

func TestMinimalContainerLoads(t *testing.T) {
	set, err := Load(writeContainer(t, validManifest(), validBlocks()))
	if err != nil {
		t.Fatal(err)
	}
	if set.Bank(1) == nil || set.Bank(1).NumChannels != 4 {
		t.Error("order-1 bank wrong")
	}
}

func TestRejections(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m map[string]any, b []testBlock) ([]byte, bool) // returns raw override
		errWant string
	}{
		{"wrong sample rate", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["sample_rate"] = 44100
			return nil, false
		}, "sample_rate"},
		{"filter_len over cap", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["filter_len"] = MaxFilterTaps + 1
			return nil, false
		}, "filter_len"},
		{"bulk delay absurd", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["bulk_delay_samples"] = 4800
			return nil, false
		}, "bulk_delay"},
		{"magls-full with alignment", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["alignment"] = map[string]any{"method": "woodworth", "head_radius_m": 0.0875}
			return nil, false
		}, "must be \"none\""},
		{"ear-aligned without method", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["filter_type"] = FilterTypeEarAligned
			return nil, false
		}, "woodworth"},
		{"ear-aligned without head radius", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["filter_type"] = FilterTypeEarAligned
			m["alignment"] = map[string]any{"method": "woodworth"}
			return nil, false
		}, "head_radius_m"},
		{"head radius over delay-ring cap", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["filter_type"] = FilterTypeEarAligned
			m["alignment"] = map[string]any{
				"method": "woodworth", "head_radius_m": 0.2, "speed_of_sound_m_s": 343.0,
			}
			return nil, false
		}, "delay ring"},
		{"unknown filter type", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["filter_type"] = "nearest-neighbour"
			return nil, false
		}, "filter_type"},
		{"listed order missing block", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["orders"] = []int{1, 2}
			return nil, false
		}, "filters_order2"},
		{"no orders", func(m map[string]any, b []testBlock) ([]byte, bool) {
			m["orders"] = []int{}
			return nil, false
		}, "no orders"},
		{"wrong filter shape", func(m map[string]any, b []testBlock) ([]byte, bool) {
			b[0].shape = []int{2, 5, 8} // 5 != (1+1)^2
			b[0].data = make([]float32, 2*5*8)
			return nil, false
		}, "shape"},
		{"ITD table mismatch", func(m map[string]any, b []testBlock) ([]byte, bool) {
			b[2].shape = []int{3}
			b[2].data = []float32{0, 0, 0}
			return nil, false
		}, "ITD table mismatch"},
		{"ITD out of sanity bound", func(m map[string]any, b []testBlock) ([]byte, bool) {
			b[2].data[0] = 0.5 // half a second: seconds/samples mix-up
			return nil, false
		}, "sanity bound"},
		{"NaN in filter taps", func(m map[string]any, b []testBlock) ([]byte, bool) {
			b[0].data[17] = float32(math.NaN())
			return nil, false
		}, "non-finite"},
		{"Inf in filter taps", func(m map[string]any, b []testBlock) ([]byte, bool) {
			b[0].data[3] = float32(math.Inf(-1))
			return nil, false
		}, "non-finite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, b := validManifest(), validBlocks()
			raw, isRaw := tc.mutate(m, b)
			if !isRaw {
				raw = writeContainer(t, m, b)
			}
			_, err := Load(raw)
			if err == nil {
				t.Fatal("Load accepted a bad container")
			}
			if !strings.Contains(err.Error(), tc.errWant) {
				t.Fatalf("error %q does not mention %q", err, tc.errWant)
			}
		})
	}
}

func TestRejectsRawCorruption(t *testing.T) {
	good := writeContainer(t, validManifest(), validBlocks())

	if _, err := Load(good[:8]); err == nil {
		t.Error("accepted truncated file")
	}

	badMagic := append([]byte{}, good...)
	badMagic[0] = 'X'
	if _, err := Load(badMagic); err == nil || !strings.Contains(err.Error(), "magic") {
		t.Errorf("bad magic: %v", err)
	}

	badVersion := append([]byte{}, good...)
	binary.LittleEndian.PutUint32(badVersion[8:], 99)
	if _, err := Load(badVersion); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("bad version: %v", err)
	}

	// A block that claims data past EOF must be rejected before reading.
	truncated := good[:len(good)-4]
	if _, err := Load(truncated); err == nil || !strings.Contains(err.Error(), "past end") {
		t.Errorf("truncated block: %v", err)
	}
}

func TestEarAlignedConsistencyAccepted(t *testing.T) {
	// The M4 shape: ear-aligned filters must name their delay model; a
	// well-formed one loads (the runtime then checks the method against its
	// configured delay source at the use site).
	m := validManifest()
	m["filter_type"] = FilterTypeEarAligned
	m["alignment"] = map[string]any{
		"method": "measured", "head_radius_m": 0.0875, "speed_of_sound_m_s": 343.0,
	}
	set, err := Load(writeContainer(t, m, validBlocks()))
	if err != nil {
		t.Fatal(err)
	}
	if set.Manifest.Alignment.Method != "measured" {
		t.Errorf("alignment method %q", set.Manifest.Alignment.Method)
	}
}
