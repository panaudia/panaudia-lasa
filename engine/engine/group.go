package engine

import (
	"sync"
	"time"

	"github.com/panaudia/panaudia-lasa/engine/buffers"
)

// SourceGroup is a lockstep set's audio-in half (lasa-core.md §4.2,
// plan/lockstep.md §4.5): one N-channel jitter buffer shared by N member
// Sources, written once per released frame with every member's audio
// interleaved, and read once per tick in prep. One joint splice moves
// every channel by the same sample count, which is the whole point:
// correlated sources stay in step through the buffer. Members keep
// their own decoders, pose rings, encoders, presence and sinks.
//
// WriteFrame runs on the connection's receive goroutine (the group
// depacketizer's output). read runs on the audio thread. Members join
// and leave under the mixer's change queue.
type SourceGroup struct {
	m  *Mixer
	id string
	n  int

	jitter *buffers.JitterBuffer

	mu      sync.Mutex // members, on join and leave
	members []*Source
	live    int

	samplesWritten uint64    // writer goroutine: the shared stream domain
	writeScratch   []float32 // 240 × n interleaved

	// Audio-thread state: the frame read this tick and its stream
	// position, consumed by every member's readFrame in the In phase.
	readScratch []float32
	readPos     uint64
}

// GroupChannel is one member's share of one released frame. Pose and
// Audio may each be absent. Lost marks a provably lost frame on this
// member: concealed with the member's decoder unless Silence says the
// member's conceal run has passed the cap, or AudioLikely says the
// loss was probably pose-only, in which case no samples are injected
// and the channel is silent for the frame.
type GroupChannel struct {
	Pose        *Pose
	Audio       []byte
	Lost        bool
	AudioLikely bool
	Silence     bool
}

func newSourceGroup(m *Mixer, spec *GroupSpec, writerFrame, readerFrame time.Duration) *SourceGroup {
	floor, robustness, capacity := buffers.QualityLevel(spec.Quality)
	if f := buffers.FECFloor(spec.Redundancy, writerFrame); f > floor {
		floor = f
	}
	return &SourceGroup{
		m: m, id: spec.ID, n: spec.Count,
		jitter: buffers.NewJitterBuffer(buffers.JitterBufferConfig{
			SampleRate:      SampleRate,
			NumChannels:     spec.Count,
			WriterFrameSize: writerFrame,
			ReaderFrameSize: readerFrame,
			Floor:           floor,
			Robustness:      robustness,
			Capacity:        capacity,
		}),
		members:      make([]*Source, spec.Count),
		writeScratch: make([]float32, FrameSize*spec.Count),
		readScratch:  make([]float32, FrameSize*spec.Count),
	}
}

// ID is the set's id, the owning client's.
func (g *SourceGroup) ID() string { return g.id }

// Count is the set's size.
func (g *SourceGroup) Count() int { return g.n }

func (g *SourceGroup) join(s *Source, index int) {
	g.mu.Lock()
	g.members[index] = s
	g.live++
	g.mu.Unlock()
}

// leave removes a member and reports whether the set is now empty.
func (g *SourceGroup) leave(index int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.members[index] != nil {
		g.members[index] = nil
		g.live--
	}
	return g.live == 0
}

// WriteFrame ingests one released frame across every member: decode
// present channels, conceal lost ones, write the interleaved frame to
// the shared buffer, then pin each member's pose at the new stream
// position. A frame with no audio on any member (every member pose-only
// or empty) writes no samples, as a pose-only packet does on a single
// source.
func (g *SourceGroup) WriteFrame(seq uint64, chans []GroupChannel) {
	anyAudio := false
	for _, c := range chans {
		if len(c.Audio) > 0 || (c.Lost && c.AudioLikely && !c.Silence) {
			anyAudio = true
			break
		}
	}
	if anyAudio {
		g.mu.Lock()
		for i := 0; i < g.n; i++ {
			var pcm []float32
			s := g.members[i]
			c := &chans[i]
			switch {
			case s == nil:
			case len(c.Audio) > 0:
				pcm = s.decodePacket(c.Audio)
			case c.Lost && c.AudioLikely && !c.Silence:
				pcm = s.concealPCM(FrameSize)
			}
			for j := 0; j < FrameSize; j++ {
				var v float32
				if j < len(pcm) {
					v = pcm[j]
				}
				g.writeScratch[j*g.n+i] = v
			}
		}
		g.mu.Unlock()
		g.jitter.Write(g.writeScratch)
		g.samplesWritten += FrameSize
	}
	g.mu.Lock()
	for i, c := range chans {
		if c.Pose != nil && g.members[i] != nil {
			g.members[i].ring.push(seq, g.samplesWritten, *c.Pose)
		}
	}
	g.mu.Unlock()
}

// read pulls this tick's frame for the whole set. Audio thread, prep
// phase, before any member's readFrame.
func (g *SourceGroup) read() {
	g.jitter.Read(g.readScratch)
	g.readPos = uint64(g.jitter.StreamReadPos())
}

// channel deinterleaves member i's samples from the frame read in prep.
func (g *SourceGroup) channel(i int, dst []float32) {
	for j := range dst {
		dst[j] = g.readScratch[j*g.n+i]
	}
}

// LatencySamples is the shared buffer's fill per channel: GetBehind
// counts interleaved floats, so one member's figure is a channel's
// share, comparable to a single source's.
func (g *SourceGroup) LatencySamples() int { return g.jitter.GetBehind() / g.n }
