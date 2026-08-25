package main

import (
	"testing"

	"github.com/panaudia/lasa/presence"
	"github.com/panaudia/lasa/wire"
	"github.com/panaudia/panaudia-lasa/engine/engine"
)

type fakePoseSink struct {
	got  []engine.Pose
	errs uint64
}

func (f *fakePoseSink) SetPose(p engine.Pose) { f.got = append(f.got, p) }
func (f *fakePoseSink) EncodeErrors() uint64  { return f.errs }

// The stats snapshot's encode_errors is the sum over the entity's live
// sinks, zero with none attached.
func TestEntityEncodeErrorsSumOverSinks(t *testing.T) {
	b := newBackend(nil)
	slot := presence.NewCollector().Register("e1", false, nil)
	rec := &entityRec{slot: slot}
	if got := rec.encodeErrors(); got != 0 {
		t.Fatalf("no sinks: encodeErrors = %d, want 0", got)
	}
	binaural := &fakePoseSink{errs: 3}
	ambi := &fakePoseSink{errs: 4}
	b.addPoseSink(rec, binaural)
	b.addPoseSink(rec, ambi)
	if got := rec.encodeErrors(); got != 7 {
		t.Fatalf("encodeErrors = %d, want 7", got)
	}
	b.removePoseSink(rec, ambi)
	if got := rec.encodeErrors(); got != 3 {
		t.Fatalf("after detach: encodeErrors = %d, want 3", got)
	}
}

// A sink attached after join (StartSink → addPoseSink) must be seeded
// with the entity's latest pose — the initial pose when no pose packet
// has arrived yet. Without this a never-moving listener renders with
// identity rotation regardless of its declared initial attitude.
func TestAddPoseSinkSeedsLatestPose(t *testing.T) {
	b := newBackend(nil)
	col := presence.NewCollector()
	slot := col.Register("e1", false, nil)
	slot.UpdatePose(wire.Pose{X: 1, Y: -2, Z: 0.5, Yaw: 0.75, Pitch: -0.1, Roll: 0.2})

	rec := &entityRec{slot: slot}
	sink := &fakePoseSink{}
	b.addPoseSink(rec, sink)

	if len(sink.got) != 1 {
		t.Fatalf("expected exactly one seeded pose, got %d", len(sink.got))
	}
	want := engine.Pose{X: 1, Y: -2, Z: 0.5, Yaw: 0.75, Pitch: -0.1, Roll: 0.2}
	got := sink.got[0]
	const eps = 1e-6 // wire pose is float32; the seed round-trips through it
	for name, pair := range map[string][2]float64{
		"x": {got.X, want.X}, "y": {got.Y, want.Y}, "z": {got.Z, want.Z},
		"yaw": {got.Yaw, want.Yaw}, "pitch": {got.Pitch, want.Pitch}, "roll": {got.Roll, want.Roll},
	} {
		if diff := pair[0] - pair[1]; diff > eps || diff < -eps {
			t.Fatalf("seeded pose %s = %v, want %v", name, pair[0], pair[1])
		}
	}

	// The seeded sink is in the fan-out list: a later pose reaches it too.
	list := rec.sinks.Load()
	if list == nil || len(*list) != 1 {
		t.Fatalf("sink not in fan-out list")
	}
}
