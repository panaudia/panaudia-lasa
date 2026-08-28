# Latency

Where the milliseconds go between a speaker's mouth and a listener's
ear through a Panaudia LASA server, and what a user should expect on
top of that from their own network and devices.

All audio in LASA is framed at 5 ms end to end (240 samples at
48 kHz): the wire frame, the server's render tick, the sink frame and
the jitter-buffer geometry are the same unit, so no stage re-frames
audio. The server's contribution is a small, near-constant number; the
one adaptive term (the ingest jitter buffer) is readable live per
source in the server's stats snapshot. Everything else that varies
lives at the edges: the user's devices, their operating system, and
the network between them and the server.

## The best case measured

Panaudia Bridge (the native macOS client) on a MacBook Pro (Apple M4
Pro) with a Focusrite Scarlett 4i4 USB interface for both input and
output, server on the same machine over loopback QUIC, binaural sink,
reverb off. Measured 2026-08-12 with a clap-and-listen loop: a clap
into the interface's mic input, through the client, the server's
spatial render, and back out of the interface, recorded and measured
in an audio editor. Single measurement, ±5 ms.

**Clap → echo: ~50 ms**, with no network in the loop.

| Stage | ms | Measured by |
|---|---|---|
| Input device: Scarlett 4i4 over USB, 5 ms io buffer + device + safety offset | 8.4 | `audioprobe`, CoreAudio properties while running |
| **Protocol block**: client Opus encode → QUIC → server → QUIC → client Opus decode | **24.2** | server chirp harness, 3 identical runs, lossless |
| &nbsp;&nbsp;&nbsp;of which server ingest jitter buffer (v4, clean feed — the adaptive term) | 5.0 | same harness |
| &nbsp;&nbsp;&nbsp;of which tick alignment, render DSP, sink encode + decode | 7.7 | engine-only chirp harness |
| &nbsp;&nbsp;&nbsp;of which client encode + server decode, transport and scheduling both ways | 11.5 | difference between the two harnesses |
| Client playout jitter buffer (v4) | ~8 | Bridge `-echo` stats |
| Output device: Scarlett 4i4, 5 ms io buffer + device + safety offset | 5.6 | `audioprobe` |
| **Measured stages** | **46.2** | |
| Remainder: acoustic gap clap→mic (~3 ms/m), capture→5 ms frame quantisation (0–5 ms), USB transport latency CoreAudio does not declare | ~4 | by subtraction |
| **Clap → echo** | **~50** | acoustic loop, ±5 ms |

The harnesses: `server/harness_test.go` `TestServerAudioLatencyChirp`
has a client encode a chirp, send it over loopback QUIC, the server
render it, and a client decode the sink and cross-correlate — 1163
samples, identical over three runs on this code, 400/400 frames
delivered with no gap events. `engine/engine/latency_test.go`
`TestAudioLatencyChirp` drives the renderer directly with PCM and a
deterministic tick, so its 12.7 ms (5.0 fill + 7.7 residual) is the
server block without the client's codec pass or any transport; the
11.5 ms between the two harnesses is that codec pass plus QUIC,
scheduling and real-clock tick alignment in both directions. The same
server harness read 30.2 ms before the v4 jitter buffer, with the
identical 19.2 ms residual, so the whole 6 ms gain is the ingest fill.

Four Opus stages sit inside the 24.2 — encode at the client, decode
at the server, encode of the rendered sink, decode at the listener —
and the harnesses do not separate them from the transport and tick
around them. What is fixed by design: at 5 ms frames each encode
carries CELT's 2.5 ms look-ahead, a decode carries none, and each
traversal waits up to one frame for assembly; the same chain at 20 ms
frames would be roughly 40 ms longer, which is the latency the 5 ms
framing decision bought.

Two things this table makes plain:

- **The server is a known, subtractable constant.** The 24.2 ms block
  is the whole protocol contribution, both jitter buffers' worth of
  codec, transport and tick included, and 12.7 of it is the server
  proper. Everything else in the ~50 is the client's playout buffer,
  the devices, and the test itself.
- **The two jitter buffers are the levers**, and both are at their
  clean-feed floors here (5.0 and ~8). On a real network they grow:
  the server's ingest buffer with the uplink's arrival jitter and
  loss, the client's playout buffer with the downlink's. That growth,
  not the server, is what "audio feels late" almost always means.

Head rotation takes a shorter path. Your own head pose does not wait
in the ingest jitter buffer (it is applied latest-wins at the next
tick), so rotation → ear on this rig is about 31 ms: pose to wire
~2.5, server apply + render + egress 15, client playout ~8, output
device ~5.6. Other people's movement deliberately rides the audio
path, so it arrives with exactly the audio's latency and stays
coherent with it — a listener who sees "slow movement" at correct
audio latency is hearing the audio latency, not a pose bug.

## What to add on top

The table above is a floor: same machine, no network, a purpose-built
interface. Real deployments add some of the following. Rough figures
are given where they are reasonably stable; the rule throughout is
that a stage either adds a fixed delay, or adds jitter which the
buffers then convert into delay.

### Network

- **Round trip.** Mouth-to-ear pays the one-way uplink from the
  speaker plus the one-way downlink to the listener — for two clients
  on similar paths, roughly one RTT to the server. Head rotation pays
  the *full* RTT of the listener's own connection (pose up, rotated
  audio down). At a 40 ms RTT the rotation loop is ~70 ms on the rig
  above; server-side pose prediction over that horizon is a planned
  mitigation, not yet shipped.
- **Jitter.** More important than average delay. The ingest and
  playout jitter buffers each servo onto the measured spread of packet
  arrival times; a path with a 20 ms arrival spread costs ~20 ms of
  buffer, on top of its average delay. Wired Ethernet is nearly
  jitter-free; **Wi-Fi is the usual source**, typically 5–30 ms of
  spread and worse under contention or power saving. Prefer a cable.
- **Loss.** LASA recovers lost 5 ms frames by full-bandwidth
  duplication (the `redundancy` field) rather than Opus in-band FEC,
  which is unavailable at 5 ms. Recovering a loss requires the playout
  point to sit at least the duplication offset deep, so a lossy path
  raises the buffer floor by that offset. The server's depacketizer
  also holds up to 8 frames (40 ms) while waiting for a late or
  duplicated frame during a gap; on a clean feed it holds nothing.
- **Mobile networks** add tens of milliseconds of RTT and large,
  bursty jitter; 4G/5G paths commonly settle at 60–150 ms of added
  latency once the buffers have adapted.
- **VPNs, proxies and QUIC-hostile middleboxes** add a hop each, and
  a network that blocks UDP blocks LASA entirely (there is no TCP
  fallback).

### Audio devices

- **Built-in laptop microphones.** On the MacBook Pro the built-in
  mic's driver DSP alone is **50 ms** (measured: 2399 frames of
  stream latency), making the input side ~56 ms against the
  Scarlett's ~8. Built-in speakers are ~22 ms running. The same
  ~50 ms client loop on built-ins measures ~110 ms, and there is
  nothing software can do about it: choose an external interface if
  latency matters.
- **USB / Thunderbolt interfaces.** The best case. A class-compliant
  interface granted 5 ms buffers gives ~14 ms total device floor in
  and out, as above. Some interfaces refuse small buffers over USB or
  add their own internal DSP; check the interface's reported latency
  and whether it honours the requested buffer size.
- **Buffer size.** Panaudia Bridge asks for 240-frame (5 ms) buffers.
  A device or OS that grants 512 or 1024 adds ~5–15 ms per side.
- **Sample-rate conversion.** A device running at 44.1 kHz or a
  system mixer resampling to 48 kHz adds a converter delay of a few
  milliseconds per side and, more importantly, a second clock that
  the jitter buffers have to drift-correct against.
- **Bluetooth.** The worst case, in two flavours. Output-only over
  A2DP (music-quality stereo) typically adds **100–250 ms**; AirPods
  and similar are in the 150–200 ms range. As soon as the microphone
  is used the headset falls to the **HFP/SCO** hands-free profile:
  mono, 8 or 16 kHz, with its own ~50–150 ms and a collapse of the
  spatial image to a single channel. A Bluetooth headset is fine for
  listening to a spatial scene and unsuitable for speaking into one
  with low latency; if it must be used, capture from a different
  microphone so the headset stays on A2DP. The browser SDK and
  Bridge detect the HFP collapse by the output sample rate dropping
  to ≤24 kHz and warn.
- **Virtual audio devices** (BlackHole, Loopback, VB-Cable,
  aggregate devices bridging clocks) add a driver hop of one buffer
  each way and a clock to reconcile. DAWs and other audio software in
  the chain add their own buffer, often 10–20 ms per hop.
- **Speaker distance.** Sound travels ~3 ms per metre; a listener a
  metre from loudspeakers hears ~3 ms later than through headphones,
  and gains an acoustic echo path back into any open mic.

### Operating system

- **macOS / CoreAudio** with the client's 5 ms buffer request is the
  measured case above. Enabling the system's *Voice Isolation* or
  *Wide Spectrum* mic modes, or any app that opens the mic in
  voice-processing mode, adds that DSP's latency (tens of ms) to the
  shared input.
- **Windows.** Shared-mode WASAPI adds the audio engine's period,
  typically 10–30 ms per side, and more on stock drivers; exclusive
  mode or ASIO with a real interface gets close to the macOS numbers.
  Consumer "audio enhancement" driver features add further DSP.
- **Linux.** PipeWire with a small quantum is comparable to CoreAudio;
  PulseAudio adds tens of milliseconds per side by default. Device
  buffer sizes granted vary widely by driver.
- **Power saving.** CPU frequency scaling and USB selective suspend
  show up as scheduling jitter, which the buffers absorb as latency;
  plug in and keep the app in the foreground for measurements.

### Echo cancellation, noise suppression and other processing

- **Acoustic echo cancellation (AEC)** operates in 10 ms blocks and
  needs a look-ahead of the rendered output; the WebRTC chain that
  browsers apply by default is ~10–20 ms, and some OS-level
  implementations more. It is only needed with open speakers; with
  headphones, turn it off.
- **Noise suppression and automatic gain control** run in the same
  10 ms-block chain, and most add a frame or two of delay. AI-based
  suppressors (Krisp, NVIDIA Broadcast, RTX Voice, macOS Voice
  Isolation) run larger analysis windows and typically add
  **20–60 ms**.
- **Bluetooth headsets' own** echo and noise processing is inside
  the HFP figure above.

### Browser clients

The browser SDK runs the same protocol and server, with the browser's
audio stack around it. Against Bridge on the same machine and devices
the measured gap is ~10 ms mouth-to-ear, made of:

- **Capture.** `getUserMedia` crosses into the browser's capture
  process with its own buffering, and unless constraints disable it,
  the WebRTC AEC/AGC/NS chain above. The capture pipeline is
  ~10–20 ms and no browser API reports it.
- **Render quantum.** AudioWorklets run in 128-frame quanta, which do
  not divide 240, so the SDK ring-buffers between the worklet clock
  and the 5 ms frame clock in both directions — up to a quantum of
  adaptation each way plus scheduling slack.
- **Deeper buffers.** Frames leave the browser on the worklet grid,
  not the ADC clock, so packets arrive at the server less evenly and
  its ingest buffer sits a few ms deeper; the browser's own playout
  buffer runs ~13 ms operating fill against Bridge's ~8.
- **Output.** `AudioContext.baseLatency` (5–10 ms on macOS Chrome at
  `latencyHint: "interactive"`) plus `outputLatency` to the device,
  which the SDK's graph report prints and which is the one browser
  figure you can read directly.

Measured to the render point on the loopback rig: 46–62 ms
mouth-to-ear and 50–53 ms for a head turn, before the output device;
~75 ms acoustic through the Scarlett and ~120 ms on built-ins, with
the v3 jitter buffer.

## Trading latency for robustness

Everything above assumes the defaults, which are tuned for the lowest
latency the network allows. An entity can ask for more robustness
through two fields of its entity definition — in the ticket for
ticketed entities, or the client's connect configuration for ad-hoc
ones (`lasa-core.md` §4.2). Both are fixed for the life of the
connection, and one declaration configures both directions of the
entity's audio: its uplink into the server and the sink rendered back
to it. They exist so each receiving end can provision its jitter
buffer before the first packet, rather than learn the need from the
first audible failure.

**`quality`** (0–2, default 0) is an intent, not a parameter: 0 is
interactive, 1 resilient, 2 playback/broadcast. The server maps it
onto its ingest buffer, and a client maps it onto its playout buffer,
as a **floor** on buffer latency and a **robustness multiplier** κ on
how much buffer the measured jitter buys:

| `quality` | Floor | κ | Meaning |
|---|---|---|---|
| 0 | none | 1 | buffer sits at the measured jitter (5 ms on a clean feed) |
| 1 | 50 ms | 4 | never below 50 ms; widens 4× further for a given measured spread |
| 2 | 150 ms | 8 | never below 150 ms; widens 8× further |

**`redundancy`** (0–7, default 0) is the maximum redundancy *offset*
this entity's path may use. With offset *n*, every audio packet also
carries a full copy of the frame *n* packets earlier, so a burst of up
to *n* consecutive lost packets (5·*n* ms) is repaired exactly, at the
cost of doubling the audio bitrate in that direction. A copy is only
useful if the receiver's buffer already spans the offset, so declaring
*n* also sets a floor on both buffers of 2 × (*n* + 2) × 5 ms — sized
for two overlapping repair holds, not one:

| `redundancy` | Covers a burst of | Buffer floor |
|---|---|---|
| 1 | 5 ms | 30 ms |
| 2 | 10 ms | 40 ms |
| 3 | 15 ms | 50 ms |
| 5 | 25 ms | 70 ms |
| 7 | 35 ms | 90 ms |

The two floors compose by **maximum**, not addition, and the floor in
turn composes by maximum with what the buffer would have chosen from
measured jitter anyway. So `quality: 1, redundancy: 3` costs 50 ms
per buffer, not 100, and on a network whose jitter already needs
60 ms it costs nothing extra. κ is the other way round: it scales the
response to *measured* spread, so it is free on a clean feed and
only spends latency where jitter exists.

**What that does to the number above.** The floors bind at both ends
— the server's ingest buffer and the client's playout buffer. The
server half is measured (`TestServerAudioLatencyChirpDeclarations`,
the chirp harness with the speaker declared as shown; the residual
19.2 ms is identical in every row, only the ingest fill moves):

| Speaker declares | Server ingest fill | Server mouth-to-ear (loopback) |
|---|---|---|
| defaults | 5.0 ms | 24.2 ms |
| `redundancy: 1` | 25.0 ms | 44.2 ms |
| `redundancy: 3` | 45.0 ms | 64.2 ms |
| `quality: 1` | ~45 ms | 64.2 ms |

The listener's playout buffer pays the same floor again on its side
(the sink half of the declaration is the client's buffer), so the
full loop roughly doubles the increment: about +40 ms at
`redundancy: 1`, +80 ms at `redundancy: 3` or `quality: 1`, and
`quality: 2` (150 ms floors, not measured here) lands near 340 ms.

Choosing:

- **Jitter alone needs no declaration.** At `quality: 0` the buffers
  already track measured arrival spread; a jittery Wi-Fi link buys
  its own latency automatically and gives it back when the spread
  falls.
- **Loss is what `redundancy` is for.** A path that drops packets
  produces clicks that no amount of buffering fixes; a copy one to
  three packets back repairs the common isolated and short-burst
  losses for 30–50 ms of latency and 2× audio bandwidth in that
  direction. Only the `redundancy` offset repairs loss exactly; the
  server's ingest otherwise conceals a lost frame with Opus PLC (up
  to ~120 ms of it, then time-slips) and holds up to 8 frames
  waiting for a late packet or its copy.
- **`quality: 1`** is for a participant who would rather be 130 ms
  late than ever hear an artefact — a remote performer on a poor
  link, a moderator who must be intelligible. **`quality: 2`** is
  for streams nobody is interacting with: a playback feed into the
  space, a broadcast sink recorded or relayed rather than listened to
  live.
- Head-tracked listening pays these floors on the rotation path too
  (the playout buffer sits in both loops), so a listener at
  `quality: 1` also feels head turns ~40 ms later.

Both fields work in both directions in all three implementations:
the server provisions its ingest buffer from them, repairs uplink
loss from the redundant copies, and emits sink redundancy at the
declared offset; Panaudia Bridge and the browser client
(`@panaudia/lasa-client`) apply the declaration to their playout
buffer, repair the sink from the copies, and emit repeats at the
declared offset on their uplink. The repair is exact:
`TestServerUplinkRedundancyRepair` drops 22 uplink datagrams of 400
(singles and bursts of two and three) under `redundancy: 3` and the
ingest reports `recovered=22 lost=0`; the same drops undeclared
report `lost=22`. The per-entity `recovered` counter is in the
`/stats` snapshot under `depacketizer`.

## Perceptual budget

For head-tracked binaural the number that matters is motion-to-sound:
the rotation loop plus the listener's own RTT. The literature's
detection thresholds are 60–110 ms for median listeners and
44–52 ms for the most sensitive, with the lower numbers applying when
a co-located real sound gives the ear a zero-latency reference (as in
AR) and the higher ones to headphone-only virtual scenes. Whether a
detectable lag actually harms the experience is a different and less
well mapped question: one study found 400 ms of tracking latency did
not degrade localisation, while a head-locked (infinitely late) image
collapsed externalisation. The rig above sits at ~31 ms rotation → ear
before the network; a 40 ms RTT puts it at ~70 ms, inside the median
budget and at the edge of the critical one.

## Measuring your own

The server's contribution is the constant above and its adaptive term
is visible live, so a latency complaint can be decomposed rather than
guessed at:

1. **Measure end to end** at the client: a clap-and-listen loop
   against your own sink (an entity may always subscribe its own sink),
   recorded and measured in an audio editor.
2. **Subtract the protocol block** — the measured 24.2 ms above:
   12.7 ms of server plus the client's codec pass and loopback
   transport. What remains is your network, your devices, your
   playout buffer, and your OS.
3. **Read the adaptive term** from the server's stats snapshot: the
   entity's `latency_samples` (÷48 for ms). Far above ~5 ms means
   uplink jitter or loss is buying latency, and the `lost`,
   `gap-events` and gap histogram alongside it say which.
4. **Check the device floor** with Bridge's `audioprobe`, which reads
   the CoreAudio latency properties of each device without opening it;
   a 50 ms stream latency on the input side is the built-in mic.
5. **Separate the pose complaints.** "My head turns feel slow" is the
   rotation loop and the listener's RTT; "other people move late" at
   correct audio sync is the audio latency itself, by design.
