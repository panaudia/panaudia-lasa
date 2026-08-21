package inout

// Engine-added file, not part of the panaudia copy (PROVENANCE.md):
// packet-loss concealment through libopus, the decode-side half of the
// lasa Depacketizer's Lost() signal (panaudia-server-design.md §3).

// ConcealFloat32 produces `samples` of concealment audio for one lost
// packet, advancing the decoder's internal state exactly as decoding
// the missing packet would have — it MUST be called in stream order, in
// place of the lost packet's Decode. Mirrors Decode's mono/stereo
// downmix; the returned slice aliases the decoder's reused buffer. On
// PLC failure the buffer is silence.
func (decoder *OpusInputDecoder) ConcealFloat32(samples int) []float32 {
	if decoder.channels == 1 {
		buf := decoder.monoBuffer[:samples]
		if err := decoder.opusDecoder.DecodePLCFloat32(buf); err != nil {
			clear(buf)
		}
		return buf
	}
	sbuf := decoder.stereoBuffer[:2*samples]
	if err := decoder.opusDecoder.DecodePLCFloat32(sbuf); err != nil {
		clear(sbuf)
	}
	for i := 0; i < samples; i++ {
		decoder.monoBuffer[i] = sbuf[i*2] + sbuf[(i*2)+1]
	}
	return decoder.monoBuffer[:samples]
}
