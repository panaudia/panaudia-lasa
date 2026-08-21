package main

import (
	"context"
	"math"
	"testing"
	"time"

	"gopkg.in/hraban/opus.v2"

	"github.com/panaudia/lasa/client"
	"github.com/panaudia/lasa/profile/base"
	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// speakToneAt publishes a sine of the given frequency and amplitude at
// the given pose as 5 ms Opus frames at the real cadence, until stop
// closes.
func speakToneAt(t *testing.T, ctx context.Context, c *client.Client, entityID string, freq float64, amp float32, pose wire.Pose, stop chan struct{}) {
	t.Helper()
	pub, err := c.Entity(ctx, entityID)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := opus.NewEncoder(engine.SampleRate, 1, opus.AppVoIP)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		pcm := make([]float32, wire.FrameSamples)
		buf := make([]byte, 1500)
		var phase float64
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for seq := uint64(0); ; seq++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			for i := range pcm {
				pcm[i] = amp * float32(math.Sin(phase))
				phase += 2 * math.Pi * freq / engine.SampleRate
			}
			n, err := enc.EncodeFloat32(pcm, buf)
			if err != nil {
				return
			}
			if pub.WriteMonoObject(seq, &wire.MonoObjectPacket{Pose: &pose, Audio: buf[:n]}) != nil {
				return
			}
		}
	}()
}

// speakTone is the common case: 440 Hz, 1 m in front of the origin.
func speakTone(t *testing.T, ctx context.Context, c *client.Client, entityID string, amp float32, stop chan struct{}) {
	t.Helper()
	speakToneAt(t, ctx, c, entityID, 440, amp, wire.Pose{Y: 1}, stop)
}

// sinkMeter turns a sink subscription into a stream of per-frame RMS
// values.
type sinkMeter struct {
	frames chan []byte
	dec    *opus.Decoder
	pcm    []float32
}

func newSinkMeter(t *testing.T, ctx context.Context, c *client.Client, entityID string) *sinkMeter {
	t.Helper()
	m := &sinkMeter{
		frames: make(chan []byte, 1024),
		pcm:    make([]float32, 2*wire.FrameSamples),
	}
	var err error
	if m.dec, err = opus.NewDecoder(engine.SampleRate, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubscribeSink(ctx, entityID, "binaural", func(seq uint64, payload []byte) {
		select {
		case m.frames <- append([]byte(nil), payload...):
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *sinkMeter) next(t *testing.T, ctx context.Context, desc string) float64 {
	t.Helper()
	var payload []byte
	select {
	case payload = <-m.frames:
	case <-ctx.Done():
		t.Fatalf("timed out reading sink frames: %s", desc)
	}
	pkt, err := wire.ParseSink(payload)
	if err != nil {
		t.Fatal(err)
	}
	n, err := m.dec.DecodeFloat32(pkt.Audio, m.pcm)
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, v := range m.pcm[:2*n] {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(2*n))
}

// waitLevel reads frames until pred holds for consec consecutive frames.
func (m *sinkMeter) waitLevel(t *testing.T, ctx context.Context, desc string, consec int, pred func(rms float64) bool) {
	t.Helper()
	run := 0
	for run < consec {
		if pred(m.next(t, ctx, desc)) {
			run++
		} else {
			run = 0
		}
	}
}

// TestStateDrivenRender is S4's acceptance: profile state writes from a
// real client steer the live render — gain changes level, the space mute
// silences, channel exclusion cuts audibility and a shared channel
// restores it. The desk client holds producer+moderator (zero entities:
// the observer/console pattern); speaker and listener are audience.
func TestStateDrivenRender(t *testing.T) {
	a, mint := startTicketedApp(t, func(cfg *appConfig) {
		cfg.Reverb = "none" // no tail: silence assertions are crisp
	})

	speaker, err := dialTicketed(t, a, "c-speak", mint("c-speak", nil, "e-speak"), "e-speak")
	if err != nil {
		t.Fatal(err)
	}
	defer speaker.Close()
	listener, err := dialTicketed(t, a, "c-listen", mint("c-listen", nil, "e-listen"), "e-listen")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	desk, err := dialObserver(t, a, "c-desk", mint("c-desk", []string{base.RoleProducer, base.RoleModerator}))
	if err != nil {
		t.Fatal(err)
	}
	defer desk.Close()
	waitFor(t, "speaker+listener live", func() bool { return settled(a, "e-speak", "e-listen") })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	meter := newSinkMeter(t, ctx, listener, "e-listen")
	stop := make(chan struct{})
	defer close(stop)
	// Amplitude 0.1 keeps the rendered sink around −25 dBFS — below the
	// engine's output-compressor knee (threshold −18 dB, knee 6 dB,
	// inout.NewStereoCompressorLimiter) — so the level assertions below
	// see the render's linear regime, not the compressor's flattened one.
	speakTone(t, ctx, speaker, "e-speak", 0.1, stop)

	// Baseline past the jitter fill.
	for i := 0; i < 50; i++ {
		meter.next(t, ctx, "jitter fill")
	}
	var b float64
	for i := 0; i < 20; i++ {
		b += meter.next(t, ctx, "baseline")
	}
	b /= 20
	if b < 0.01 {
		t.Fatalf("baseline too quiet to test against: %v", b)
	}

	write := func(desc string, msgs ...wire.StateMessage) {
		t.Helper()
		if err := desk.WriteState(ctx, msgs...); err != nil {
			t.Fatalf("%s: %v", desc, err)
		}
	}

	// Gain changes level: gain is an amplitude multiplier (profile §6,
	// pinned 2026-08-04), so 0.25 quarters the rendered level —
	// audibly attenuated, provably not silence.
	write("set gain", wire.Set{Key: "lasa.entity.e-speak.render.gain", Value: []byte("0.25")})
	meter.waitLevel(t, ctx, "quarter level", 10, func(r float64) bool { return r > 0.15*b && r < 0.35*b })
	write("clear gain", wire.Clear{Key: "lasa.entity.e-speak.render.gain"})
	meter.waitLevel(t, ctx, "gain restored", 10, func(r float64) bool { return r > 0.8*b })

	// The space mute silences the source in every mix; clearing restores.
	write("mute", wire.Set{Key: "lasa.space.entity.muted.e-speak", Value: []byte("true")})
	meter.waitLevel(t, ctx, "muted", 10, func(r float64) bool { return r < 0.05*b })
	write("unmute", wire.Clear{Key: "lasa.space.entity.muted.e-speak"})
	meter.waitLevel(t, ctx, "unmuted", 10, func(r float64) bool { return r > 0.8*b })

	// Channel exclusion: the speaker leaves main for stage — no shared
	// unmuted channel with the listener, so silence; the listener's sink
	// joining stage restores audibility.
	write("move speaker to stage",
		wire.Set{Key: "lasa.entity.e-speak.heard-in.stage", Value: []byte("true")},
		wire.Clear{Key: "lasa.entity.e-speak.heard-in.main"})
	meter.waitLevel(t, ctx, "channel-excluded", 10, func(r float64) bool { return r < 0.05*b })
	write("listener joins stage", wire.Set{Key: "lasa.entity.e-listen.hears.stage", Value: []byte("true")})
	meter.waitLevel(t, ctx, "shared stage channel", 10, func(r float64) bool { return r > 0.8*b })
}

// TestPairSemanticsE2E: the pair record through the composed server —
// channel gain and attenuation overrides audibly resolved per pair
// (§4), and the §6 personal mute/solo semantics (bidirectional mute,
// solo wins) driven by real clients' state writes.
func TestPairSemanticsE2E(t *testing.T) {
	a, mint := startTicketedApp(t, func(cfg *appConfig) {
		cfg.Reverb = "none"
	})
	speaker, err := dialTicketed(t, a, "c-speak", mint("c-speak", nil, "e-speak"), "e-speak")
	if err != nil {
		t.Fatal(err)
	}
	defer speaker.Close()
	listener, err := dialTicketed(t, a, "c-listen", mint("c-listen", nil, "e-listen"), "e-listen")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	desk, err := dialObserver(t, a, "c-desk", mint("c-desk", []string{base.RoleProducer}))
	if err != nil {
		t.Fatal(err)
	}
	defer desk.Close()
	waitFor(t, "both live", func() bool { return settled(a, "e-speak", "e-listen") })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	meter := newSinkMeter(t, ctx, listener, "e-listen")
	stop := make(chan struct{})
	defer close(stop)
	// 2 m away: distance attenuation is visible (exponent 2 halves the
	// amplitude at d=2), and the level stays below the compressor knee.
	speakToneAt(t, ctx, speaker, "e-speak", 440, 0.1, wire.Pose{Y: 2}, stop)

	for i := 0; i < 50; i++ {
		meter.next(t, ctx, "jitter fill")
	}
	var b float64
	for i := 0; i < 20; i++ {
		b += meter.next(t, ctx, "baseline")
	}
	b /= 20
	if b < 0.005 {
		t.Fatalf("baseline too quiet to test against: %v", b)
	}
	writeAs := func(c *client.Client, desc string, msgs ...wire.StateMessage) {
		t.Helper()
		if err := c.WriteState(ctx, msgs...); err != nil {
			t.Fatalf("%s: %v", desc, err)
		}
	}

	// Channel gain doubles the pair's level (§4: winner × render.gain).
	writeAs(desk, "channel gain", wire.Set{Key: "lasa.space.channel.gain.main", Value: []byte("2.0")})
	meter.waitLevel(t, ctx, "channel gain 2.0", 10, func(r float64) bool { return r > 1.6*b && r < 2.4*b })
	writeAs(desk, "clear channel gain", wire.Clear{Key: "lasa.space.channel.gain.main"})
	meter.waitLevel(t, ctx, "channel gain cleared", 10, func(r float64) bool { return r > 0.8*b && r < 1.2*b })

	// Channel attenuation 0 removes the distance law: at 2 m with the
	// default exponent 2 the amplitude was halved, so flat doubles it.
	writeAs(desk, "channel attenuation", wire.Set{Key: "lasa.space.channel.attenuation.main", Value: []byte("0")})
	meter.waitLevel(t, ctx, "flat distance law", 10, func(r float64) bool { return r > 1.6*b && r < 2.4*b })
	writeAs(desk, "clear channel attenuation", wire.Clear{Key: "lasa.space.channel.attenuation.main"})
	meter.waitLevel(t, ctx, "distance law restored", 10, func(r float64) bool { return r > 0.8*b && r < 1.2*b })

	// Personal mute, listener side: silence for the muter.
	writeAs(listener, "listener mutes speaker", wire.Set{Key: "lasa.entity.e-listen.mute.e-speak", Value: []byte("true")})
	meter.waitLevel(t, ctx, "muted by listener", 10, func(r float64) bool { return r < 0.05*b })
	writeAs(listener, "unmute", wire.Clear{Key: "lasa.entity.e-listen.mute.e-speak"})
	meter.waitLevel(t, ctx, "unmuted", 10, func(r float64) bool { return r > 0.8*b })

	// Bidirectional: the SPEAKER muting the listener also silences the
	// listener's ears toward the speaker (§6, pinned 2026-08-04).
	writeAs(speaker, "speaker mutes listener", wire.Set{Key: "lasa.entity.e-speak.mute.e-listen", Value: []byte("true")})
	meter.waitLevel(t, ctx, "muted by the speaker (bidirectional)", 10, func(r float64) bool { return r < 0.05*b })
	writeAs(speaker, "speaker unmutes", wire.Clear{Key: "lasa.entity.e-speak.mute.e-listen"})
	meter.waitLevel(t, ctx, "unmuted again", 10, func(r float64) bool { return r > 0.8*b })

	// Solo wins over a personal mute (§6): mute + solo → audible.
	writeAs(listener, "mute and solo",
		wire.Set{Key: "lasa.entity.e-listen.mute.e-speak", Value: []byte("true")},
		wire.Set{Key: "lasa.entity.e-listen.solo.e-speak", Value: []byte("true")})
	meter.waitLevel(t, ctx, "solo beats mute", 10, func(r float64) bool { return r > 0.8*b })
	writeAs(listener, "clear solo, mute bites", wire.Clear{Key: "lasa.entity.e-listen.solo.e-speak"})
	meter.waitLevel(t, ctx, "mute applies after solo clears", 10, func(r float64) bool { return r < 0.05*b })
	writeAs(listener, "clear mute", wire.Clear{Key: "lasa.entity.e-listen.mute.e-speak"})
	meter.waitLevel(t, ctx, "audible again", 10, func(r float64) bool { return r > 0.8*b })

	// render.directivity (engine cardioid law): the speaker faces +X
	// with the listener at 90° — full directivity silences the pair,
	// half renders at half amplitude.
	writeAs(desk, "full directivity", wire.Set{Key: "lasa.entity.e-speak.render.directivity", Value: []byte("1")})
	meter.waitLevel(t, ctx, "cardioid null at 90°", 10, func(r float64) bool { return r < 0.05*b })
	writeAs(desk, "half directivity", wire.Set{Key: "lasa.entity.e-speak.render.directivity", Value: []byte("0.5")})
	meter.waitLevel(t, ctx, "half directivity at 90°", 10, func(r float64) bool { return r > 0.35*b && r < 0.65*b })
	writeAs(desk, "clear directivity", wire.Clear{Key: "lasa.entity.e-speak.render.directivity"})
	meter.waitLevel(t, ctx, "omni again", 10, func(r float64) bool { return r > 0.8*b })

	// render.size: the listener 2 m from an 8 m source is inside it —
	// enveloping, audibly present, not a silence and not a blow-up.
	writeAs(desk, "large size", wire.Set{Key: "lasa.entity.e-speak.render.size", Value: []byte("8")})
	meter.waitLevel(t, ctx, "enveloping size", 10, func(r float64) bool { return r > 0.1*b && r < 3*b })
	writeAs(desk, "clear size", wire.Clear{Key: "lasa.entity.e-speak.render.size"})
	meter.waitLevel(t, ctx, "point source again", 10, func(r float64) bool { return r > 0.8*b && r < 1.2*b })
}
