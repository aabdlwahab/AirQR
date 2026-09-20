package audio

import (
	"encoding/binary"
	"fmt"
	"io"
)

// WriteWAV writes mono 16-bit PCM. Every browser decodes this without a codec,
// which keeps the web receiver free of any audio dependency.
func WriteWAV(w io.Writer, samples []float32, sampleRate int) error {
	data := make([]byte, len(samples)*2)
	for i, s := range samples {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(s*32767)))
	}
	hdr := make([]byte, 0, 44)
	hdr = append(hdr, "RIFF"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(36+len(data)))
	hdr = append(hdr, "WAVEfmt "...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 16)
	hdr = binary.LittleEndian.AppendUint16(hdr, 1) // PCM
	hdr = binary.LittleEndian.AppendUint16(hdr, 1) // mono
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(sampleRate))
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(sampleRate*2))
	hdr = binary.LittleEndian.AppendUint16(hdr, 2)
	hdr = binary.LittleEndian.AppendUint16(hdr, 16)
	hdr = append(hdr, "data"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(len(data)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// ReadWAV reads mono or multi-channel 16-bit PCM, mixing down to mono.
func ReadWAV(raw []byte) ([]float32, int, error) {
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("not a RIFF/WAVE file")
	}
	var channels, bits int
	var rate int
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if body+size > len(raw) {
			size = len(raw) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, fmt.Errorf("short fmt chunk")
			}
			channels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
		case "data":
			if bits != 16 || channels < 1 {
				return nil, 0, fmt.Errorf("only 16-bit PCM is supported (got %d-bit, %d channels)", bits, channels)
			}
			frames := size / (2 * channels)
			out := make([]float32, frames)
			for i := 0; i < frames; i++ {
				sum := 0.0
				for c := 0; c < channels; c++ {
					v := int16(binary.LittleEndian.Uint16(raw[body+(i*channels+c)*2:]))
					sum += float64(v) / 32768
				}
				out[i] = float32(sum / float64(channels))
			}
			return out, rate, nil
		}
		pos = body + size
		if size%2 == 1 {
			pos++
		}
	}
	return nil, 0, fmt.Errorf("no data chunk")
}
