package main

// The S6 acceptance instruments: the engine's chirp and pose harnesses
// ported to run through the WHOLE composed server — a real lasa client
// publishing over loopback QUIC, depacketizer, jitter, spatial render,
// binaural encode, sink track back to a real client. Mouth-to-ear minus
// network and capture/playback. The numbers logged here are the server
// baseline; the assertions are tripwires, not specs.

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/hraban/opus.v2"

	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/engine"
)

// sinkCapture collects a sink track's frames with their sequence
// numbers for lossless, order-recoverable analysis.
type sinkCapture struct {
	mu     sync.Mutex
	frames []capturedFrame
}

type capturedFrame struct {
	seq     uint64
	payload []byte
}

func (sc *sinkCapture) handler(seq uint64, payload []byte) {
	sc.mu.Lock()
	sc.frames = append(sc.frames, capturedFrame{seq: seq, payload: append([]byte(nil), payload...)})
	sc.mu.Unlock()
}

func (sc *sinkCapture) count() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return len(sc.frames)
}

// sorted returns the captured frames in sequence order. Loopback rarely
// reorders, but the analysis must not depend on that.
func (sc *sinkCapture) sorted() []capturedFrame {
	sc.mu.Lock()
	out := append([]capturedFrame(nil), sc.frames...)
	sc.mu.Unlock()
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].seq < out[j-1].seq; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// monoStream decodes a sorted capture to one continuous mono stream
// (L+R summed), inserting silence for any missing sequence numbers so
// sample positions stay aligned.
func monoStream(t *testing.T, frames []capturedFrame) []float32 {
	t.Helper()
	dec, err := opus.NewDecoder(engine.SampleRate, 2)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, 2*wire.FrameSamples)
	var out []float32
	for i, f := range frames {
		if i > 0 {
			for miss := frames[i-1].seq + 1; miss < f.seq; miss++ {
				out = append(out, make([]float32, wire.FrameSamples)...)
			}
		}
		pkt, err := wire.ParseSink(f.payload)
		if err != nil {
			t.Fatal(err)
		}
		n, err := dec.DecodeFloat32(pkt.Audio, pcm)
		if err != nil {
			t.Fatal(err)
		}
		for s := 0; s < n; s++ {
			out = append(out, pcm[2*s]+pcm[2*s+1])
		}
	}
	return out
}

// stereoRMS decodes a sorted capture to per-frame L/R RMS series
// (missing seqs become 0,0 — they break dominance holds, never fake
// them).
func stereoRMS(t *testing.T, frames []capturedFrame) (rmsL, rmsR []float64) {
	t.Helper()
	dec, err := opus.NewDecoder(engine.SampleRate, 2)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, 2*wire.FrameSamples)
	for i, f := range frames {
		if i > 0 {
			for miss := frames[i-1].seq + 1; miss < f.seq; miss++ {
				rmsL, rmsR = append(rmsL, 0), append(rmsR, 0)
			}
		}
		pkt, err := wire.ParseSink(f.payload)
		if err != nil {
			t.Fatal(err)
		}
		n, err := dec.DecodeFloat32(pkt.Audio, pcm)
		if err != nil {
			t.Fatal(err)
		}
		var l, r float64
		for s := 0; s < n; s++ {
			l += float64(pcm[2*s]) * float64(pcm[2*s])
			r += float64(pcm[2*s+1]) * float64(pcm[2*s+1])
		}
		rmsL = append(rmsL, math.Sqrt(l/float64(n)))
		rmsR = append(rmsR, math.Sqrt(r/float64(n)))
	}
	return
}

// firstFrom returns the first index >= start where pred holds for
// `hold` consecutive indices; -1 if never (engine harness helper).
func firstFrom(start, hold, n int, pred func(int) bool) int {
	if start < 0 {
		start = 0
	}
	for i := start; i < n; i++ {
		ok := true
		for j := i; j < i+hold && j < n; j++ {
			if !pred(j) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func (a *app) entityStat(t *testing.T, id string) entityStats {
	t.Helper()
	for _, e := range a.stats().Entities {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no stats for entity %q", id)
	return entityStats{}
}

// TestServerAudioLatencyChirp is the engine chirp harness through the
// composed server: mouth-to-ear = client opus encode → loopback QUIC →
// depacketizer → jitter → render → binaural opus → sink track → client
// decode. The jitter-fill term comes from the S6 stats surface so the
// residual is visible.
func TestServerAudioLatencyChirp(t *testing.T) {
	if testing.Short() {
		t.Skip("latency harness is slow")
	}
	a := startTestApp(t, func(cfg *appConfig) { cfg.Reverb = "none" })
	speaker, err := dialTest(t, a, "c-speak", "e-speak")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := dialTest(t, a, "c-listen", "e-listen")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both live", func() bool { return settled(a, "e-speak", "e-listen") })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cap := &sinkCapture{}
	if _, err := listener.SubscribeSink(ctx, "e-listen", "binaural", cap.handler); err != nil {
		t.Fatal(err)
	}

	// A linear chirp (100 Hz → 6 kHz over 2 s) as 400 5 ms frames,
	// dead ahead of the origin listener (median plane: equal ITDs).
	const chirpFrames = 400
	chirp := make([]float32, chirpFrames*wire.FrameSamples)
	const f0, f1 = 100.0, 6000.0
	T := float64(len(chirp)) / engine.SampleRate
	for i := range chirp {
		tt := float64(i) / engine.SampleRate
		phase := 2 * math.Pi * (f0*tt + (f1-f0)/(2*T)*tt*tt)
		chirp[i] = float32(0.5 * math.Sin(phase))
	}

	pub, err := speaker.Entity(ctx, "e-speak")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := opus.NewEncoder(engine.SampleRate, 1, opus.AppAudio)
	if err != nil {
		t.Fatal(err)
	}
	var fill int
	buf := make([]byte, 1500)
	pose := wire.Pose{X: 2}
	tick := time.NewTicker(5 * time.Millisecond)
	for p := 0; p < chirpFrames; p++ {
		<-tick.C
		n, err := enc.EncodeFloat32(chirp[p*wire.FrameSamples:(p+1)*wire.FrameSamples], buf)
		if err != nil {
			t.Fatal(err)
		}
		if err := pub.WriteMonoObject(uint64(p), &wire.MonoObjectPacket{Pose: &pose, Audio: buf[:n]}); err != nil {
			t.Fatal(err)
		}
		if p == chirpFrames*3/4 { // steady-state jitter term, mid-stream
			fill = a.entityStat(t, "e-speak").LatencySamples
		}
	}
	tick.Stop()
	time.Sleep(400 * time.Millisecond) // drain the tail through the pipe

	out := monoStream(t, cap.sorted())
	if len(out) < 60000 {
		t.Fatalf("too little output audio: %d samples", len(out))
	}

	// Cross-correlate a mid-signal window against the output; |peak| so
	// an HRTF polarity flip cannot hide the alignment.
	const refStart, refLen, maxLag = 24000, 48000, 12000
	best, bestLag := 0.0, -1
	for lag := 0; lag < maxLag; lag++ {
		var acc float64
		for i := 0; i < refLen; i++ {
			oi := refStart + lag + i
			if oi >= len(out) {
				break
			}
			acc += float64(chirp[refStart+i]) * float64(out[oi])
		}
		if a := math.Abs(acc); a > best {
			best, bestLag = a, lag
		}
	}
	if bestLag < 0 {
		t.Fatal("no correlation peak found")
	}

	dep := a.entityStat(t, "e-speak").Depacketizer
	ms := func(samples int) float64 { return float64(samples) * 1000.0 / engine.SampleRate }
	residual := bestLag - fill
	t.Logf("server mouth-to-ear: %d samples (%.1f ms) = jitter fill %d (%.1f ms) + residual %d (%.1f ms — transport, tick alignment, DSP, double opus)",
		bestLag, ms(bestLag), fill, ms(fill), residual, ms(residual))
	t.Logf("ingress: delivered=%d lost=%d skipped=%d gap-events=%d", dep.Delivered, dep.Lost, dep.Skipped, dep.GapEvents)

	// Tripwires — generous, not a spec.
	if bestLag < 240 || bestLag > 12000 {
		t.Errorf("measured latency %d samples (%.1f ms) outside the sane range 5–250 ms", bestLag, ms(bestLag))
	}
	if residual < 0 {
		t.Errorf("residual negative (%d samples): measurement broken?", residual)
	}
	if dep.Delivered < 300 {
		t.Errorf("ingress delivered only %d of %d frames", dep.Delivered, chirpFrames)
	}
}

// TestServerSinkPoseLatency: the head-turn path through the server. A
// 2 kHz source sits hard left; the listener's pose-only packets flip
// yaw by π; the latency is measured in received sink frames between the
// flip being sent and the binaural image flipping ears.
func TestServerSinkPoseLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency harness is slow")
	}
	a := startTestApp(t, func(cfg *appConfig) { cfg.Reverb = "none" })
	speaker, err := dialTest(t, a, "c-tone", "e-tone")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := dialTest(t, a, "c-head", "e-head")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both live", func() bool { return settled(a, "e-tone", "e-head") })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	speakToneAt(t, ctx, speaker, "e-tone", 2000, 0.15, wire.Pose{Y: 2}, stop)

	cap := &sinkCapture{}
	if _, err := listener.SubscribeSink(ctx, "e-head", "binaural", cap.handler); err != nil {
		t.Fatal(err)
	}

	// The listener publishes pose-only packets at the real cadence; the
	// yaw value is flipped mid-stream (latest-wins at the sink slot).
	var yawBits atomic.Uint64
	headPub, err := listener.Entity(ctx, "e-head")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for seq := uint64(0); ; seq++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			pose := wire.Pose{Yaw: math.Float32frombits(uint32(yawBits.Load()))}
			if headPub.WriteMonoObject(seq, &wire.MonoObjectPacket{Pose: &pose}) != nil {
				return
			}
		}
	}()

	waitFor(t, "warmup frames", func() bool { return cap.count() >= 250 })
	mark := cap.count()
	yawBits.Store(uint64(math.Float32bits(math.Pi))) // about-face
	waitFor(t, "post-flip frames", func() bool { return cap.count() >= mark+150 })

	rmsL, rmsR := stereoRMS(t, cap.sorted())
	n := len(rmsL)
	pre := firstFrom(50, 10, n, func(i int) bool { return rmsL[i] > rmsR[i]*1.2 })
	if pre < 0 || pre > mark-20 {
		t.Fatalf("no stable left dominance before the flip (first at %d, mark %d)", pre, mark)
	}
	flip := firstFrom(mark-2, 10, n, func(i int) bool { return rmsR[i] > rmsL[i]*1.2 })
	if flip < 0 {
		t.Fatal("image never flipped after the pose step")
	}
	latencyFrames := flip - mark
	t.Logf("server sink-pose latency: %d frames (%.1f ms — datagram, latest-wins slot, render, sink frame in flight)",
		latencyFrames, float64(latencyFrames)*5)
	if latencyFrames < -2 || latencyFrames > 40 {
		t.Errorf("sink-pose latency %d frames outside -2..40 (0–200 ms)", latencyFrames)
	}
}

// TestServerSourcePoseCoherence: the stream-speed identity through the
// server — a source's pose step must be heard at the same distance from
// its tone onset as the two were sent in the stream, whatever the
// jitter latency happens to be (freshness deliberately traded for
// audio/pose sync).
func TestServerSourcePoseCoherence(t *testing.T) {
	if testing.Short() {
		t.Skip("latency harness is slow")
	}
	a := startTestApp(t, func(cfg *appConfig) { cfg.Reverb = "none" })
	speaker, err := dialTest(t, a, "c-mover", "e-mover")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := dialTest(t, a, "c-ear", "e-ear")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both live", func() bool { return settled(a, "e-mover", "e-ear") })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cap := &sinkCapture{}
	if _, err := listener.SubscribeSink(ctx, "e-ear", "binaural", cap.handler); err != nil {
		t.Fatal(err)
	}

	// One stream, two marked events: tone onset at frame toneAt, pose
	// step left→right at frame stepAt. The pose rides the packets.
	const total, toneAt, stepAt = 320, 100, 200
	pub, err := speaker.Entity(ctx, "e-mover")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := opus.NewEncoder(engine.SampleRate, 1, opus.AppVoIP)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, wire.FrameSamples)
	buf := make([]byte, 1500)
	var phase float64
	tick := time.NewTicker(5 * time.Millisecond)
	for p := 0; p < total; p++ {
		<-tick.C
		for i := range pcm {
			pcm[i] = 0
			if p >= toneAt {
				pcm[i] = 0.15 * float32(math.Sin(phase))
				phase += 2 * math.Pi * 2000 / engine.SampleRate
			}
		}
		pose := wire.Pose{Y: 2} // hard left
		if p >= stepAt {
			pose = wire.Pose{Y: -2} // hard right
		}
		n, err := enc.EncodeFloat32(pcm, buf)
		if err != nil {
			t.Fatal(err)
		}
		if err := pub.WriteMonoObject(uint64(p), &wire.MonoObjectPacket{Pose: &pose, Audio: buf[:n]}); err != nil {
			t.Fatal(err)
		}
	}
	tick.Stop()
	time.Sleep(400 * time.Millisecond)

	rmsL, rmsR := stereoRMS(t, cap.sorted())
	n := len(rmsL)
	var peak float64
	for i := range rmsL {
		if v := rmsL[i] + rmsR[i]; v > peak {
			peak = v
		}
	}
	onset := firstFrom(0, 5, n, func(i int) bool { return rmsL[i]+rmsR[i] > peak*0.1 })
	if onset < 0 {
		t.Fatal("tone never heard")
	}
	flip := firstFrom(onset, 10, n, func(i int) bool { return rmsR[i] > rmsL[i]*1.2 })
	if flip < 0 {
		t.Fatal("pose step never heard")
	}

	heard := (flip - onset) * wire.FrameSamples
	sent := int((float64(stepAt)+0.5)*wire.FrameSamples) - toneAt*wire.FrameSamples
	skew := heard - sent
	t.Logf("server pose/audio coherence: sent gap %d samples, heard gap %d, skew %d (%.1f ms)",
		sent, heard, skew, float64(skew)*1000/engine.SampleRate)
	// Engine tripwire was ±720; the server adds real-clock feed jitter
	// and possible adaptive time-slips, so ±1200 (25 ms).
	if skew < -1200 || skew > 1200 {
		t.Errorf("pose/audio skew %d samples outside ±1200 — jitter alignment broken?", skew)
	}
}
