# engine: the binaural rendering pipeline

This directory holds the spatial audio engine behind LASA's `binaural`
sink: the code that turns many mono sources at known positions into one
stereo headphone feed per listener. It is written for readers who know
binaural rendering and ambisonic decoding already. It summarises the
choices made here and where each spatial cue is produced, and it points
at the code and design documents rather than repeating them.

The engine runs on the server. Every listener gets their own render,
computed every 5 ms at 48 kHz (240-sample frames), and the result is
encoded to stereo Opus. Clients only ever receive stereo.

## Bilateral ambisonics

The render is bilateral: each listener owns two ambisonic buses, one
per ear, and each bus is decoded to its own ear by a static
spherical-harmonic-domain (SH) convolver. This differs from the usual
single-field decode in two ways that matter.

First, the buses are ear-centred, not head-centred. A source is encoded
into the left bus from the direction the left ear sees it, and into the
right bus from the direction the right ear sees it. For far sources the
two directions coincide and one SH evaluation is shared. Inside the
near-field gate they differ, and that difference is the parallax cue.

Second, the interaural time difference (ITD) is not in the decode
filters at all. The decode filters are ear-aligned (delay-removed), and
the ITD is applied at encode as an explicit per-(listener, source, ear)
fractional delay on the source signal before it is packed into the bus.
This is exact at any distance and any order, and it removes the
truncation-error interaction between low-order SH and the interaural
phase that ordinary ambisonic-to-binaural decoding suffers from
(the BiMagLS line of work, Ben-Hur et al. and Engel et al.).

The consequence is a strict division of labour. Everything geometric,
temporal, and interaural lives at encode, where it is evaluated
analytically per source per frame. Everything spectral lives at decode,
where it is a fixed filter set shared by every listener. The decode
stage never sees a direction or a rotation.

Per listener, per frame, the pipeline is:

```
for each audible source:
  geometry in the listener's head frame
    head-centre distance d and lateral angle
    per-ear directions (parallax projection, d < 2 m)
    per-ear Woodworth delays
  per ear:
    x_e  = fractional delay(source, itd_e)
    x_e  = nfc_e(x_e)                       if d < 2 m
    pack x_e and its SH weight vector into bus e
bus_L = W_L · X_L      bus_R = W_R · X_R    (two GEMMs)
late reverb added to the first-order channels of both buses
out_L = SHconv(bus_L, H_L)   out_R = SHconv(bus_R, H_R)
```

The mix is a matrix multiply per bus over all packed sources, so the
per-source cost is the SH evaluation, the delay read, and (near field
only) two biquads. The delay and the biquads are fused into the pack
copy, so a near source costs one read and one write per sample.

## Where each cue is produced

| Cue | Stage | Mechanism |
|---|---|---|
| Direction | encode | analytic real SH evaluation in the head frame, up to order 5 |
| Head rotation | encode | listener rotation applied to the source vector before SH evaluation, crossfaded per frame |
| ITD | encode | Woodworth spherical-head delay, per ear, fractional, ramped across the frame |
| Distance gain | encode | 1/d^α with α the source attenuation, unity at 1 m, capped at +9.5 dB |
| Parallax | encode | per-ear source directions inside 2 m, projected onto the HRTF measurement sphere |
| Near-field ILD and spectral tilt | encode | Duda-Martens difference filters, two biquads per ear, inside 2 m |
| Source extent and directivity | encode | per-order SH cap weighting for size, cardioid family for directivity |
| Far-field ILD and head shadow | decode | magnitude of the ear-aligned HRTF filters |
| Pinna and elevation cues | decode | magnitude of the ear-aligned HRTF filters |
| Diffuse-field equalisation | decode | baked into the filter set offline |
| Late reverberation | post-mix | one shared reverb per listener, decorrelated between ears |

Rotation is worth a note. It is not an SH rotation matrix folded into
the decoder. It is a 3×3 rotation of each listener-to-source vector
before the SH evaluation that the encode performs anyway, so rotation
costs nothing extra and is exact for point sources. The decode filters
are therefore static and shared across all listeners in the process.
Per-listener decode state is the convolver overlap tail only.

## The head model

The engine ships one head. The default filter set is derived from the
TH Köln Neumann KU100 measurements (Bernschütz 2013, CC BY 3.0),
measured at 3.25 m on a dense Lebedev grid. It was chosen over the
alternatives (SADIE II KU100 and KEMAR, the legacy SAF set) for three
reasons: it is the set most of the bilateral and BiMagLS literature
validates on, its grid is the densest available, and its measurement
radius is genuinely far-field, which makes it a clean reference for the
near-field difference filters. The same group's near-field KU100
compilation (0.25 to 1.5 m) gives a measured check on the model.

There is deliberately no runtime personalisation. A single
non-individualised dummy head gives every listener the same rendering,
which keeps listening tests comparable and keeps the decode filters
shareable across all listeners on a server. The loader is built to
accept other sets in the same container format, so a custom set is a
matter of compiling one offline, not of changing the engine.

Filter design is done entirely offline by the `panaudia-hrtf` tool,
which is the only component that ever reads SOFA. It emits a
`PAHRTF` container (JSON manifest plus float32 blocks) and the runtime
embeds that file. The shipped set is `thk-ku100-bimagls-v1`:

| Property | Value |
|---|---|
| Design | BiMagLS, ear-aligned, max-rE weighting, diffuse-field EQ |
| MagLS transition | 1.5 kHz |
| Orders carried | 2, 3, 4, 5 |
| Filter length | 272 taps at 48 kHz |
| Bulk delay | 36 samples (0.75 ms) |
| Alignment model | Woodworth, radius 0.115 m fitted to the measured ITDs, c = 343 m/s |
| Third-octave magnitude error | ≤ 0.45 dB across all orders |
| Residual below-cutoff ITD | ≤ 0.25 samples |

Below the MagLS transition the filters keep the measured interaural
phase, so the explicit encode-side delay carries the envelope ITD and
the residual low-frequency ITD stays in the filters where it was
measured. Above the transition the design is magnitude-only, as usual.

The alignment model is an invariant, not a setting. The offline tool
removes exactly the Woodworth delays it records in the manifest, and the
runtime reinserts exactly those delays from the manifest. The filter
set and the delay model switch together, and the loader refuses a set
whose alignment method does not match the runtime's delay model. Getting
this wrong is the double-ITD trap, and the design closes it at load
time.

Two head radii appear in the code and they are different on purpose.
The Woodworth radius (0.115 m) is an ITD-estimator fit and overstates
the physical head because the estimator measures the low-frequency
ITD. The geometric ear offset (0.0875 m) places the ears for the
parallax projection and normalises distance for the near-field table.

## Near field: 2 m

Inside a head-centre distance of 2 m a source gets the full near-field
treatment, and beyond it none. The gate blends the near-field filters
linearly to identity between 1.8 m and 2.0 m so entering and leaving
the region is click-free, and beyond 2 m the near-field code is not
executed at all.

Three things happen inside the gate.

Parallax gives each ear its own direction. Each ear sits at ± 0.0875 m
on the interaural axis, and the ear-to-source ray is projected onto the
sphere of the HRTF measurement radius (3.25 m) centred on the head. The
direction under which the measured set best represents that ray is the
one used for the SH evaluation. Beyond 2 m the angular split between
the two ears is under about 2.5°, far below the resolution of the
orders in use, so one shared evaluation is used.

Near-field compensation corrects the interaural level difference (ILD)
and the low-frequency emphasis that a rigid sphere produces close to
the source. The filters are Duda-Martens difference filters: the sphere
response at distance d divided by the response at the measurement
distance, so they express only the deviation from the far-field HRTF
that the decode already applies. Each is fitted offline as two cascaded
biquads over a grid of (1/ρ, θ), where ρ is distance over ear offset and
θ is the angle between the ear ray and the source ray. One table serves
both ears by symmetry. At runtime the coefficients are interpolated
bilinearly, and the generator asserts pole stability at every grid
point and every interpolation midpoint, with a worst fit error of
0.35 dB. Because the distance axis is normalised by ear offset the
table is head-radius generic.

The distance law is 1/d^α referenced to 1 m, where α is the source's
attenuation exponent, and the near-field boost is capped at 3.0 (about
+9.5 dB, reached near 0.58 m at the default exponent) so proximity
carries level as well as the spectral and ILD cues.

The ITD is not gated. Woodworth delays apply at all distances, since a
far source still needs its interaural delay.

## Order

The pipeline is order-generic. Every stage (SH evaluation, pack and
GEMM, decode convolution) is written for (N+1)² channels, and the
shipped filter set carries banks for orders 2 to 5. The order is fixed
per server at startup (`PANAUDIA_ORDER`, default 3) and applies to the
whole space. Fifth order is 36 channels per bus, 72 per listener.

The bilateral design is what makes higher order cheap enough to be
worth having. Because the ITD is exact regardless of order, raising
the order buys only spectral and localisation resolution, and the cost
grows with the two GEMMs and the number of forward FFTs in the decode,
not with any per-listener filter design.

## Late reverberation

Each listener has one late reverb (a comb and allpass network on the
omnidirectional premix), fed through a dry/wet split that moves toward
wet with distance and with source size. The wet signal is added to the
first-order channels of both ear buses. With ear-aligned decode filters
there is no interaural phase left in the decoder to decorrelate it, so
the right bus's wet signal passes through a fixed velvet-noise sparse
FIR (18 signed taps over 20 ms, energy-normalised). Measured wet-tail
interaural correlation is close to zero. Only the wet signal is
decorrelated. The dry buses must stay identical in everything but
their per-source spatial weights.

## Decode and latency

Each ear bus is decoded by a single-partition overlap-save convolver:
one 512-point FFT per SH channel, a complex multiply-accumulate against
that ear's static filter spectra, and one inverse FFT per ear. The
filters fit the 512-point block with a 240-sample hop, so there is no
filterbank latency. The whole binaural path adds the filter set's bulk
delay (0.75 ms) plus a constant ITD base delay of about 0.4 ms that
keeps every per-ear read causal. The convolver has a vDSP backend on
macOS and a vendored PFFFT backend everywhere else, both validated
against one float64 oracle.

Pose for sources is tightly correleated to source audio, 
they are carried in the same packet, and the jitter buffer interpolates on read to calculate the 
correct position aligned to the last sample of the read buffer.

Rotation happens at encode on the server, so head-tracking latency
includes the network loop, but it uses the latest available known pose, 
reducing rotation latency compared to the audio path.

## Where things live

| Package | Contents |
|---|---|
| `ambisonic/` | encoder geometry, SH evaluation, ITD delays, parallax, NFC, extent, reverb, GEMM mix |
| `binaural/` | the per-listener bilateral decoder built on `convolver/` |
| `convolver/` | overlap-save SH convolver (cgo, vDSP or PFFFT) |
| `hrtf/` | the `PAHRTF` loader and the embedded default set |
| `gemm/` | matrix-multiply backends behind the mix |
| `engine/` | the render loop, entities, poses, channel policy |
| `common/` | shared constants including the head-model parameters |

The design record is in the `panaudia` repository under
`plan/near-field-compensation/` (the plan, the ten design decisions,
the research survey, and the M3 design notes that chose the head), and
the filter tool and its validation gates are in `panaudia-hrtf`.
