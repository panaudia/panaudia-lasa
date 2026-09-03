package main

// The ceiling: how many users who both speak and listen this
// machine's composed server pipeline holds at real time, with the
// network taken out and the codecs either in or out. What runs is the
// real backend: EntityJoined → Depacketizer → dof enforcement + pose
// fan-out → (Opus decode) → jitter write; the render at its 5 ms
// cadence under the server's own clock; StartSink → bilateral decode
// + dynamics → (Opus encode) → sinkBridge → SinkWriter. What is
// stubbed: QUIC/MoQ — pre-framed packets are handed straight to each
// entity's EntityHandler at the frame cadence by a few feeder
// goroutines standing in for the per-connection receive goroutines.
//
// Two variants. TestCeilingCodecFree swaps both codecs for raw float32
// (engine.SourceCodecRawF32 / SinkCodecRawF32 through the backend's
// codec hatch): the render and buffers alone. TestCeilingOpus leaves
// the production codecs in: every packet is a real mono Opus frame
// decoded at ingest and every sink frame is a real coupled-stereo Opus
// encode. Client-side encoding is done once up front (a 16-frame Opus
// cycle per entity) so the feeders' per-tick cost is server ingest
// only.
//
// Unlike the 50×50 smoke (fixed N, a pass/fail) this ramps N and
// reports the last step that held, so the number is a property of the
// machine it ran on. The only assertion is the design floor: 50 must
// hold. The rows are the product; append them to
// lasa-planning/plan/latency-baseline.md's capacity table.
//
// Known departures from the real load, all on the cheap side: a
// handful of ingest goroutines instead of one per connection, no
// presence subscribers, no state traffic, and no datagram send.

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/hraban/opus.v2"

	"github.com/panaudia/lasa/connect"
	"github.com/panaudia/lasa/server"
	"github.com/panaudia/lasa/wire"

	"github.com/panaudia/panaudia-lasa/engine/engine"
	"github.com/panaudia/panaudia-lasa/engine/gemm"
	"github.com/panaudia/panaudia-lasa/engine/timing"
)

// ceilingWriter counts a sink's frames; the sampled one also keeps the
// last frame's RMS so the harness can prove audio actually flowed
// (decoding it first when the sink codec is Opus).
type ceilingWriter struct {
	frames atomic.Uint64
	rms    atomic.Uint64 // float64 bits
	sample bool
	dec    *opus.Decoder // sampled + Opus sinks only
	pcm    []float32
}

func (w *ceilingWriter) WriteFrame(frame []byte, _ uint64) error {
	w.frames.Add(1)
	if !w.sample {
		return nil
	}
	var acc float64
	var n int
	if w.dec != nil {
		per, err := w.dec.DecodeFloat32(frame, w.pcm)
		if err != nil {
			return nil
		}
		n = 2 * per
		for _, v := range w.pcm[:n] {
			acc += float64(v) * float64(v)
		}
	} else {
		n = len(frame) / 4
		for i := 0; i < n; i++ {
			v := float64(math.Float32frombits(binary.LittleEndian.Uint32(frame[4*i:])))
			acc += v * v
		}
	}
	w.rms.Store(math.Float64bits(math.Sqrt(acc / float64(n))))
	return nil
}

type ceilingEntity struct {
	id   string
	h    server.EntityHandler
	sess server.SinkSession
	w    *ceilingWriter
	pkts [][]byte // pre-framed mono-object packets, cycled by seq
	seq  uint64
}

// ceilingPackets pre-frames K packets for one entity: a pose on a
// circle and one 5 ms slice each of a sine whose period divides the
// K-frame cycle, so the cycle is seamless (the Opus cycle carries a
// small encoder-state discontinuity at the wrap, which is load-neutral).
// Framing and client-side encoding are done once, so the feeders'
// per-tick cost is server ingest only.
const ceilingCycle = 16

func ceilingPackets(t *testing.T, i, n int, useOpus bool) [][]byte {
	t.Helper()
	ang := 2 * math.Pi * float64(i) / float64(n)
	pose := wire.Pose{X: float32(3 * math.Cos(ang)), Y: float32(3 * math.Sin(ang))}
	freq := 12.5 * float64(24+i%64) // multiples of 12.5 Hz cycle every 16 frames
	var enc *opus.Encoder
	if useOpus {
		var err error
		if enc, err = opus.NewEncoder(engine.SampleRate, 1, opus.AppAudio); err != nil {
			t.Fatal(err)
		}
	}
	pkts := make([][]byte, ceilingCycle)
	pcm := make([]float32, wire.FrameSamples)
	raw := make([]byte, 4*wire.FrameSamples)
	opusBuf := make([]byte, 1500)
	for f := 0; f < ceilingCycle; f++ {
		for s := 0; s < wire.FrameSamples; s++ {
			pcm[s] = float32(0.05 * math.Sin(2*math.Pi*freq*float64(f*wire.FrameSamples+s)/engine.SampleRate))
		}
		var audio []byte
		if useOpus {
			sz, err := enc.EncodeFloat32(pcm, opusBuf)
			if err != nil {
				t.Fatal(err)
			}
			audio = opusBuf[:sz]
		} else {
			for s, v := range pcm {
				binary.LittleEndian.PutUint32(raw[4*s:], math.Float32bits(v))
			}
			audio = raw
		}
		pkt, err := wire.AppendMonoObject(nil, &wire.MonoObjectPacket{Pose: &pose, Audio: audio})
		if err != nil {
			t.Fatal(err)
		}
		pkts[f] = pkt
	}
	return pkts
}

// ceilingFeeder is the stand-in for N receive goroutines: a few
// workers, each a loop at the frame cadence handing its share of the
// entities their next packet. Sharding matters once Opus decode is in
// the ingest path: serialised on one goroutine it would saturate before
// the render does, which the real server's per-connection goroutines
// do not.
const ceilingFeedWorkers = 4

type ceilingFeeder struct {
	workers []*ceilingFeedWorker
	next    int
	stop    chan struct{}
	wg      sync.WaitGroup
}

type ceilingFeedWorker struct {
	mu    sync.Mutex
	ents  []*ceilingEntity
	ticks atomic.Int64
	busy  atomic.Int64 // ns
	max   atomic.Int64 // ns
}

func newCeilingFeeder() *ceilingFeeder {
	f := &ceilingFeeder{stop: make(chan struct{})}
	for i := 0; i < ceilingFeedWorkers; i++ {
		w := &ceilingFeedWorker{}
		f.workers = append(f.workers, w)
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			w.run(f.stop)
		}()
	}
	return f
}

func (w *ceilingFeedWorker) run(stop <-chan struct{}) {
	tk := timing.NewTicker(tickMillis, false)
	for {
		select {
		case <-stop:
			return
		default:
		}
		start := time.Now()
		w.mu.Lock()
		for _, e := range w.ents {
			e.h.WritePacket(e.seq, e.pkts[e.seq%ceilingCycle])
			e.seq++
		}
		w.mu.Unlock()
		d := int64(time.Since(start))
		w.ticks.Add(1)
		w.busy.Add(d)
		for {
			m := w.max.Load()
			if d <= m || w.max.CompareAndSwap(m, d) {
				break
			}
		}
		tk.Tick()
	}
}

func (f *ceilingFeeder) add(e *ceilingEntity) { f.addSet([]*ceilingEntity{e}) }

// addSet places a lockstep set's members on ONE worker: the group
// depacketizer is single-writer by the one-connection rule, and one
// worker advancing every member once per tick is what keeps their
// seqs equal, as the bridge's encode pump does.
func (f *ceilingFeeder) addSet(set []*ceilingEntity) {
	w := f.workers[f.next%len(f.workers)]
	f.next++
	w.mu.Lock()
	w.ents = append(w.ents, set...)
	w.mu.Unlock()
}

func (f *ceilingFeeder) close() {
	close(f.stop)
	f.wg.Wait()
}

// snapshot returns the workers' (busy ns, ticks) totals and resets max.
func (f *ceilingFeeder) snapshot() (busy, ticks []int64) {
	for _, w := range f.workers {
		busy = append(busy, w.busy.Load())
		ticks = append(ticks, w.ticks.Load())
		w.max.Store(0)
	}
	return
}

func (f *ceilingFeeder) maxBusy() time.Duration {
	var m int64
	for _, w := range f.workers {
		if v := w.max.Load(); v > m {
			m = v
		}
	}
	return time.Duration(m)
}

type ceilingRow struct {
	variant                 string
	n                       int
	elapsed                 time.Duration
	ticks, expectTicks      int64
	prep, in, across, out   time.Duration
	feedAvg, feedMax        time.Duration
	minFrames, expectFrames uint64
	underruns               int64
	maxFill                 int
	rms                     float64
	holds                   bool
}

func (r ceilingRow) String() string {
	total := r.prep + r.in + r.across + r.out
	return fmt.Sprintf("%s N=%d: render %d/%d ticks, per tick prep=%s in=%s across=%s out=%s (total %s = %.0f%% of 5 ms); feeder worker avg %s max %s; sink frames min %d/%d; jitter underruns %d, max fill %d smp; sampled rms %.4f; holds=%v",
		r.variant, r.n, r.ticks, r.expectTicks, r.prep, r.in, r.across, r.out, total, 100*float64(total)/float64(5*time.Millisecond),
		r.feedAvg, r.feedMax, r.minFrames, r.expectFrames, r.underruns, r.maxFill, r.rms, r.holds)
}

// TestCeilingCodecFree: the render and buffers alone, both codecs
// swapped for raw float32.
func TestCeilingCodecFree(t *testing.T) { runCeiling(t, false, 0) }

// TestCeilingOpus: the production codecs in — mono Opus decode per
// packet at ingest, coupled-stereo Opus encode per sink frame.
func TestCeilingOpus(t *testing.T) { runCeiling(t, true, 0) }

// ceilingSet is the lockstep variants' set size: every step in the
// ramp is a multiple of it, so every set is complete.
const ceilingSet = 5

// TestCeilingCodecFreeLockstep / TestCeilingOpusLockstep: the same
// ramp with the entities admitted as lockstep sets of ceilingSet
// (lasa-core.md §4.2), so each set's ingest is one group depacketizer
// releasing N-channel frames into one shared jitter buffer, read once
// per tick in prep. Compared against the independent rows at the same
// N, the difference is lockstep's capacity price (plan/lockstep.md).
func TestCeilingCodecFreeLockstep(t *testing.T) { runCeiling(t, false, ceilingSet) }
func TestCeilingOpusLockstep(t *testing.T)      { runCeiling(t, true, ceilingSet) }

func runCeiling(t *testing.T, useOpus bool, setSize int) {
	if testing.Short() {
		t.Skip("ceiling harness is slow")
	}
	if raceEnabled {
		t.Skip("capacity numbers are meaningless under the race detector")
	}
	variant := "codec-free"
	if useOpus {
		variant = "opus"
	}
	if setSize > 0 {
		variant += fmt.Sprintf(" lockstep%d", setSize)
	}
	steps := []int{50, 75, 100, 125, 150, 175, 200, 250, 300}
	maxN := steps[len(steps)-1]
	// Knobs for one-off runs (no code edit): CEILING_ORDER=2..5,
	// CEILING_REVERB=none|tight-room|small-room|medium-room|... The
	// defaults are the server's defaults, so the baseline rows are
	// the production configuration.
	var cfg appConfig
	a := startTestApp(t, func(c *appConfig) {
		c.MaxEntities = maxN + 8
		if v := os.Getenv("CEILING_ORDER"); v != "" {
			if o, err := strconv.Atoi(v); err == nil {
				c.Order = o
			}
		}
		if v := os.Getenv("CEILING_REVERB"); v != "" {
			c.Reverb = v
		}
		cfg = *c
	})
	if !useOpus {
		a.backend.sourceCodec = engine.SourceCodecRawF32
		a.backend.sinkCodec = engine.SinkCodecRawF32
	}
	t.Logf("%s: engine order %d, workers %d, reverb %s, gemm backend %s (target %s), %d CPUs, %d feed workers",
		variant, cfg.Order, cfg.Workers, cfg.Reverb, gemm.Backend, gemm.Target(), runtime.NumCPU(), ceilingFeedWorkers)

	feeder := newCeilingFeeder()
	var ents []*ceilingEntity
	// clientOf is the entity's owning client: its own for independent
	// entities, its set's for lockstep members.
	clientOf := func(i int, id string) string {
		if setSize > 0 {
			return fmt.Sprintf("c-set-%03d", i/setSize)
		}
		return "c-" + id
	}
	defer func() {
		feeder.close()
		for i, e := range ents {
			e.sess.Stop()
			a.backend.EntityLeft(clientOf(i, e.id), e.id)
		}
		waitFor(t, "all swept", func() bool { return settled(a) })
	}()

	const measure = 3 * time.Second
	var rows []ceilingRow
	ceiling := 0
	for _, n := range steps {
		var set []*ceilingEntity
		for i := len(ents); i < n; i++ {
			id := fmt.Sprintf("e-%03d", i)
			re := connect.ResolvedEntity{Entity: connect.Entity{ID: id, Name: id}}
			if setSize > 0 {
				re.Lockstep = &connect.LockstepMember{Index: i % setSize, Count: setSize}
			}
			h, err := a.backend.EntityJoined(clientOf(i, id), re)
			if err != nil {
				t.Fatalf("join %s: %v", id, err)
			}
			w := &ceilingWriter{sample: i == 0}
			if w.sample && useOpus {
				if w.dec, err = opus.NewDecoder(engine.SampleRate, 2); err != nil {
					t.Fatal(err)
				}
				w.pcm = make([]float32, 2*wire.FrameSamples)
			}
			sess, err := a.backend.StartSink(id, "binaural", w)
			if err != nil {
				t.Fatalf("sink %s: %v", id, err)
			}
			e := &ceilingEntity{id: id, h: h, sess: sess, w: w, pkts: ceilingPackets(t, i, maxN, useOpus)}
			ents = append(ents, e)
			if setSize == 0 {
				feeder.add(e)
				continue
			}
			set = append(set, e)
			if len(set) == setSize {
				feeder.addSet(set)
				set = nil
			}
		}
		if len(set) != 0 {
			t.Fatalf("step %d is not a multiple of the set size %d", n, setSize)
		}
		waitFor(t, fmt.Sprintf("%d admitted", n), func() bool { return settled(a, idsOf(ents)...) })
		time.Sleep(500 * time.Millisecond) // buffers fill, audibility settles

		s0 := a.mixer.Stats()
		f0 := make([]uint64, n)
		for i, e := range ents {
			f0[i] = e.w.frames.Load()
		}
		st0 := a.stats()
		fb0, ft0 := feeder.snapshot()
		wall := time.Now()
		time.Sleep(measure)
		elapsed := time.Since(wall)
		s1 := a.mixer.Stats()
		st1 := a.stats()
		feedMax := feeder.maxBusy()
		fb1, ft1 := feeder.snapshot()

		r := ceilingRow{variant: variant, n: n, elapsed: elapsed, ticks: s1.Ticks - s0.Ticks}
		r.expectTicks = int64(elapsed / (5 * time.Millisecond))
		r.expectFrames = uint64(r.expectTicks)
		if dt := r.ticks; dt > 0 {
			r.prep = (s1.Prep - s0.Prep) / time.Duration(dt)
			r.in = (s1.In - s0.In) / time.Duration(dt)
			r.across = (s1.Across - s0.Across) / time.Duration(dt)
			r.out = (s1.Out - s0.Out) / time.Duration(dt)
		}
		for i := range fb1 { // the busiest worker's average
			if ft := ft1[i] - ft0[i]; ft > 0 {
				if avg := time.Duration((fb1[i] - fb0[i]) / ft); avg > r.feedAvg {
					r.feedAvg = avg
				}
			}
		}
		r.feedMax = feedMax
		r.minFrames = math.MaxUint64
		for i, e := range ents {
			if d := e.w.frames.Load() - f0[i]; d < r.minFrames {
				r.minFrames = d
			}
		}
		under := map[string]int64{}
		for _, e := range st0.Entities {
			under[e.ID] = e.Jitter.Underruns
		}
		for _, e := range st1.Entities {
			r.underruns += e.Jitter.Underruns - under[e.ID]
			if e.LatencySamples > r.maxFill {
				r.maxFill = e.LatencySamples
			}
		}
		r.rms = math.Float64frombits(ents[0].w.rms.Load())
		total := r.prep + r.in + r.across + r.out
		r.holds = float64(r.ticks) >= 0.95*float64(r.expectTicks) &&
			total <= 5*time.Millisecond &&
			float64(r.minFrames) >= 0.9*float64(r.expectFrames) &&
			r.feedAvg <= 5*time.Millisecond &&
			r.rms > 1e-4
		rows = append(rows, r)
		t.Log(r.String())
		if !r.holds {
			break
		}
		ceiling = n
	}
	t.Logf("%s ceiling on this machine: N=%d users speaking and listening (last step that held; steps %v)", variant, ceiling, steps)
	if rows[0].n == 50 && !rows[0].holds {
		t.Errorf("the design floor of 50 speaking+listening users does not hold (%s): %s", variant, rows[0])
	}
}

func idsOf(ents []*ceilingEntity) []string {
	ids := make([]string, len(ents))
	for i, e := range ents {
		ids[i] = e.id
	}
	return ids
}
