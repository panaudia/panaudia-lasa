package engine

import (
	"math"
	"testing"
)

func testConfig() Config {
	return Config{Order: 3, MaxEntities: 8, ReverbPreset: ReverbNone, Workers: 4}
}

type collectingWriter struct {
	frames [][]byte
	ts     []uint64
}

func (c *collectingWriter) WriteFrame(opus []byte, sampleTS uint64) {
	c.frames = append(c.frames, append([]byte(nil), opus...))
	c.ts = append(c.ts, sampleTS)
}

func TestToneReachesSink(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if _, err := m.AddSource("speaker", SourceConfig{
		TestTone:    440,
		InitialPose: Pose{X: 2, Y: 0, Z: 0},
	}); err != nil {
		t.Fatal(err)
	}
	w := &collectingWriter{}
	if _, err := m.AddSink("listener", w); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		m.Process()
	}

	if len(w.frames) == 0 {
		t.Fatal("no frames reached the sink")
	}
	for i, f := range w.frames {
		if len(f) == 0 {
			t.Fatalf("empty opus frame %d", i)
		}
	}
	for i := 1; i < len(w.ts); i++ {
		if w.ts[i]-w.ts[i-1] != FrameSize {
			t.Fatalf("sampleTS not contiguous: %d -> %d", w.ts[i-1], w.ts[i])
		}
	}

	// The listener's rendered bus must carry energy (white-box: the test
	// goroutine is the audio thread, so reading entity state is safe).
	e := m.ents["listener"]
	var energy float64
	for _, v := range e.enc.Output {
		energy += float64(v) * float64(v)
	}
	if energy == 0 {
		t.Fatal("listener bus is silent")
	}
}

func TestEntityMutedSilencesSource(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if _, err := m.AddSource("speaker", SourceConfig{TestTone: 440, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("listener", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.Process()
	}

	if err := m.SetEntityMuted("speaker", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.Process()
	}

	e := m.ents["listener"]
	var energy float64
	for _, v := range e.enc.Output {
		energy += float64(v) * float64(v)
	}
	if energy != 0 {
		t.Fatalf("muted source still audible: energy %v", energy)
	}

	if err := m.SetEntityMuted("speaker", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.Process()
	}
	energy = 0
	for _, v := range e.enc.Output {
		energy += float64(v) * float64(v)
	}
	if energy == 0 {
		t.Fatal("unmuted source inaudible")
	}
}

func TestWritePCMPoseAlignment(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	src, err := m.AddSource("mover", SourceConfig{InitialPose: Pose{X: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("listener", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}

	// Feed 100 frames of audio with a pose that walks +X 0.01 m per
	// 240-sample packet, then render until the jitter buffer has played
	// most of it. The encoder's source position must have advanced well
	// past the initial pose and must never overshoot the newest pose —
	// jitter-aligned, not fresh.
	frame := make([]float32, FrameSize)
	for i := range frame {
		frame[i] = float32(math.Sin(float64(i) * 0.1))
	}
	for i := 1; i <= 100; i++ {
		p := Pose{X: 1 + float64(i)*0.01}
		src.WritePCM(uint64(i), &p, frame)
	}
	for i := 0; i < 90; i++ {
		m.Process()
	}

	e := m.ents["mover"]
	got := e.enc.Position.X // Size=1: this IS metres
	if got <= 1.05 {
		t.Fatalf("source position never advanced: %v", got)
	}
	if got > 2.0 {
		t.Fatalf("source position overshot newest pose: %v", got)
	}
	if src.Loudness() <= 0 {
		t.Fatal("loudness not measured")
	}
}

func TestSinkPoseFreshAndListenerSplit(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// A both-roles entity: source position stays jitter-aligned while
	// the listener position tracks the fresh sink pose independently.
	src, err := m.AddSource("walker", SourceConfig{InitialPose: Pose{X: 1}})
	if err != nil {
		t.Fatal(err)
	}
	sink, err := m.AddSink("walker", &collectingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	_ = src

	sink.SetPose(Pose{X: 9, Yaw: 0.5})
	m.Process()

	e := m.ents["walker"]
	if lp := e.enc.ListenerPositionMeters(); lp.X != 9 {
		t.Fatalf("listener position not fresh: %v", lp)
	}
	// Source-role position untouched by the sink pose (still at the
	// initial pose — no audio has played).
	if e.enc.Position.X != 1 {
		t.Fatalf("source position contaminated by sink pose: %v", e.enc.Position)
	}
}

func TestLifecycleAndCapacity(t *testing.T) {
	m, err := New(Config{Order: 3, MaxEntities: 2, ReverbPreset: ReverbNone, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if _, err := m.AddSource("a", SourceConfig{TestTone: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSource("a", SourceConfig{TestTone: 100}); err != ErrDuplicateSource {
		t.Fatalf("want ErrDuplicateSource, got %v", err)
	}
	// Same entity may still take a sink; a second entity fills capacity.
	if _, err := m.AddSink("a", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("a", &collectingWriter{}); err != ErrDuplicateSink {
		t.Fatalf("want ErrDuplicateSink, got %v", err)
	}
	if _, err := m.AddSource("b", SourceConfig{TestTone: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSource("c", SourceConfig{TestTone: 100}); err != ErrFull {
		t.Fatalf("want ErrFull, got %v", err)
	}

	if err := m.SetRenderParams("nobody", DefaultRenderParams()); err != ErrUnknownEntity {
		t.Fatalf("want ErrUnknownEntity, got %v", err)
	}

	// Remove b entirely; capacity frees; slot is reusable after a tick.
	m.RemoveSource("b")
	m.Process()
	if _, err := m.AddSource("c", SourceConfig{TestTone: 100}); err != nil {
		t.Fatalf("slot not freed: %v", err)
	}
	m.Process()

	// Entity "a" survives source removal while its sink lives.
	m.RemoveSource("a")
	m.Process()
	if m.ents["a"] == nil {
		t.Fatal("entity with live sink vanished on source removal")
	}
	m.RemoveSink("a")
	m.Process()
	if m.ents["a"] != nil {
		t.Fatal("entity not removed after both halves gone")
	}
	// Idempotent removes.
	m.RemoveSink("a")
	m.RemoveSource("a")
	m.Process()
}

func TestCloseDrainsPendingTeardown(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	// A removal enqueued but never ticked: Close must run it (the
	// queued closure holds cgo resources the GC cannot free)...
	if _, err := m.AddSink("gone", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	m.Process()
	m.RemoveSink("gone") // no Process after this

	// ...and a sink still attached at Close must be destroyed inline.
	if _, err := m.AddSink("attached", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	m.Process()

	m.Close()

	// Post-Close the closing goroutine is the audio thread's successor,
	// so reading audio-side state is safe.
	if m.ents["gone"] != nil {
		t.Fatal("queued RemoveSink did not run during Close")
	}
	if e := m.ents["attached"]; e == nil || e.sink != nil {
		t.Fatal("attached sink not destroyed during Close")
	}
	m.Close() // idempotent
}

func TestRenderParamsApplied(t *testing.T) {
	m, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if _, err := m.AddSource("s", SourceConfig{TestTone: 200, InitialPose: Pose{X: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSink("l", &collectingWriter{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.Process()
	}
	e := m.ents["l"]
	var base float64
	for _, v := range e.enc.Output {
		base += float64(v) * float64(v)
	}

	p := DefaultRenderParams()
	p.Gain = 0.0625 // -24 dB
	if err := m.SetRenderParams("s", p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.Process()
	}
	var quiet float64
	for _, v := range e.enc.Output {
		quiet += float64(v) * float64(v)
	}
	if quiet >= base/2 {
		t.Fatalf("gain change had no effect: base %v quiet %v", base, quiet)
	}
}
