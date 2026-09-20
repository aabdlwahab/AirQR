// Package audio carries AirQR transfers over an acoustic channel.
//
// The modulation is 16-tone continuous-phase MFSK: each 4-bit nibble selects
// one of 16 orthogonal tones, and the transmit phase runs unbroken across
// symbol boundaries so no windowing or fade is needed between symbols.
//
// A transmission is a sequence of independent blocks:
//
//	[sync tone][frame symbols][silence]  [sync tone][frame symbols][silence]  ...
//
// Every block re-syncs from its own sync tone, so clock drift never
// accumulates and a block lost to a cough or a notification costs exactly one
// frame. That matters because the frames carry AIRQR2 fountain symbols: the
// receiver needs any K independent frames, not any particular ones, so a
// dropped block is an erasure the fountain layer already knows how to absorb.
//
// Frames are protected by a CRC only, not by FEC. A frame either verifies and
// becomes a fountain symbol or it is discarded — which turns the channel's bit
// errors into the clean erasures the fountain code wants. The cost is that a
// frame with a single bad symbol is thrown away whole; adding Reed-Solomon
// inside the frame would recover those, at the price of a second coding layer
// to keep in sync between Go and JavaScript.
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Tones is the MFSK alphabet size: one tone per 4-bit nibble.
const Tones = 16

// Band describes where in the spectrum a transmission sits and how fast it
// runs. Spacing must be at least 1/SymbolSec for the tones to stay orthogonal
// over one symbol, which is what lets the receiver pick them apart with a
// plain non-coherent Goertzel.
type Band struct {
	Name      string
	Base      float64 // frequency of tone 0 / subcarrier 0, Hz
	Spacing   float64 // gap between adjacent tones, Hz
	SymbolSec float64 // serial: one symbol. parallel: the useful symbol window.
	SyncSec   float64 // duration of the per-frame sync tone
	GapSec    float64 // silence after each frame

	// Carriers > 0 selects parallel mode: that many simultaneous
	// differentially-keyed QPSK subcarriers instead of one tone at a time.
	// See ofdm.go for why that is the only way to go substantially faster.
	Carriers  int
	PrefixSec float64 // cyclic prefix, absorbs early reflections
	SuffixSec float64 // cyclic suffix, gives the taper somewhere to land

	// Parity is how many Reed-Solomon parity bytes ride with each frame,
	// correcting up to Parity/2 corrupt bytes. Zero leaves the frame protected
	// by its CRC alone, which is the original wire format.
	Parity int

	// Phase is bits per subcarrier per symbol: 2 (QPSK) or 3 (8-PSK). Zero
	// means 2.
	Phase int
}

// wrapFrame prepares a frame for the air: a triplicated length byte, the frame,
// then Reed-Solomon parity over it.
//
// The length is sent three times because of a genuine ordering problem: the
// receiver must know how many bytes to read before it can run the correction
// that would have fixed a corrupt length byte. Three copies land on different
// subcarriers, so a single dead one is outvoted.
func wrapFrame(frame []byte, parity int) []byte {
	if parity <= 0 {
		return frame
	}
	out := make([]byte, 0, 3+len(frame)+parity)
	l := byte(len(frame))
	out = append(out, l, l, l)
	out = append(out, frame...)
	return append(out, RSEncode(frame, parity)...)
}

// majority returns the value at least two of three agree on.
func majority(a, b, c byte) byte {
	if a == b || a == c {
		return a
	}
	if b == c {
		return b
	}
	return a
}

// maxFrameLen is the largest frame that still leaves room for parity inside a
// 255-byte Reed-Solomon codeword.
func maxFrameLen(parity int) int {
	if parity <= 0 {
		return 255
	}
	return 255 - parity
}

// Bands are the presets offered on the command line. The ultrasonic band sits
// at 19.0-19.8 kHz: measured on a MacBook Pro that region is both the
// strongest part of the speaker response and the quietest part of the ambient
// spectrum, and it stays clear of the deep null near 18.75 kHz.
var Bands = map[string]Band{
	"fast":       {Name: "fast", Base: 1500, Spacing: 200, SymbolSec: 0.006, SyncSec: 0.060, GapSec: 0.040},
	"audible":    {Name: "audible", Base: 1200, Spacing: 100, SymbolSec: 0.012, SyncSec: 0.060, GapSec: 0.040},
	"ultrasonic": {Name: "ultrasonic", Base: 19000, Spacing: 50, SymbolSec: 0.024, SyncSec: 0.080, GapSec: 0.040},

	// Parallel bands occupy the same spectrum as their serial namesake but
	// carry 2 bits on each of many subcarriers at once, so they run several
	// times faster at the same symbol time.
	//
	// The 12 ms cyclic prefix is the one number worth understanding. It has to
	// outlast the room's longest significant reflection, because anything
	// arriving later smears one symbol into the next. Measured against a
	// simulated channel, a 4 ms prefix loses every frame once a 13 ms echo is
	// present while a 12 ms prefix recovers all of them, and the extra guard
	// costs only about a quarter of the throughput. 12 ms corresponds to a
	// path difference of roughly four metres, which covers an ordinary room.
	"audible-fast": {Name: "audible-fast", Base: 1200, Spacing: 50, SymbolSec: 0.020,
		SyncSec: 0.060, GapSec: 0.040, Carriers: 32, PrefixSec: 0.012, SuffixSec: 0.002, Parity: 16},
	"ultrasonic-fast": {Name: "ultrasonic-fast", Base: 19000, Spacing: 50, SymbolSec: 0.020,
		SyncSec: 0.080, GapSec: 0.040, Carriers: 16, PrefixSec: 0.012, SuffixSec: 0.002, Parity: 16},
	"ultrasonic-wide": {Name: "ultrasonic-wide", Base: 19000, Spacing: 50, SymbolSec: 0.020,
		SyncSec: 0.080, GapSec: 0.040, Carriers: 32, PrefixSec: 0.012, SuffixSec: 0.002, Parity: 16},

	// ultrawide takes the whole usable window and three bits per subcarrier.
	//
	// The upper edge is not the speaker's doing: a sweep shows output holding
	// to within 6 dB at 21.8 kHz and then collapsing 27 dB by 22.0 kHz and
	// 113 dB by 23.0 kHz. That cliff is the 48 kHz reconstruction filter, and
	// nothing on this side of it can be used. Below 19 kHz sits a deep null
	// around 18.75 kHz. So 19.0-21.8 kHz is all there is, and the sync tone at
	// 21.6 kHz sits just inside it.
	//
	// It stops at 44 subcarriers rather than filling the window to the brim.
	// Past that the margin falls off a cliff: at 48 the simulated near-field
	// delivery drops from 7 frames in 7 to 4, and at 52 even a clean channel
	// loses most of them. Two things bite at once — each extra subcarrier takes
	// a share of a fixed power budget, and a higher crest factor drags the
	// whole transmission down further when it is normalised away from clipping.
	// 44 is the last count that still holds its margin.
	//
	// It buys speed by spending margin twice over besides: 8-PSK halves the
	// angular distance between decision boundaries, and the prefix guards
	// against less reverberation than the other parallel bands. It wants a
	// short, direct path, and carries extra parity because forty-four
	// subcarriers cross more nulls than sixteen.
	"ultrawide": {Name: "ultrawide", Base: 19000, Spacing: 50, SymbolSec: 0.020,
		SyncSec: 0.080, GapSec: 0.040, Carriers: 44, PrefixSec: 0.008, SuffixSec: 0.002,
		Parity: 24, Phase: 3},
}

// BandNames lists the presets in a stable order for help text and for the
// receiver's band search.
var BandNames = []string{"fast", "audible", "ultrasonic", "audible-fast", "ultrasonic-fast", "ultrasonic-wide", "ultrawide"}

// ToneFreq returns the carrier for symbol i.
func (b Band) ToneFreq(i int) float64 { return b.Base + float64(i)*b.Spacing }

// SyncFreq sits one slot above the top data tone, so it can never be confused
// with a data symbol and still lands inside the band the speaker reproduces
// well.
func (b Band) SyncFreq() float64 {
	if b.Parallel() {
		return b.Base + float64(b.Carriers)*b.Spacing
	}
	return b.Base + float64(Tones)*b.Spacing
}

// Validate reports whether the band's tones stay orthogonal and fit below
// Nyquist at the given sample rate.
func (b Band) Validate(sampleRate int) error {
	if b.Parallel() {
		return b.validateParallel(sampleRate)
	}
	if b.Spacing*b.SymbolSec < 0.999 {
		return fmt.Errorf("band %s: spacing %.0f Hz is too narrow for a %.0f ms symbol (need >= %.0f Hz)",
			b.Name, b.Spacing, b.SymbolSec*1000, 1/b.SymbolSec)
	}
	if top := b.SyncFreq(); top >= float64(sampleRate)/2 {
		return fmt.Errorf("band %s: top tone %.0f Hz is at or above Nyquist %.0f Hz",
			b.Name, top, float64(sampleRate)/2)
	}
	return nil
}

// Frame types.
const (
	FrameBeacon = 0x01 // transfer metadata: K, T, hash, sizes
	FrameData   = 0x02 // one fountain symbol
)

// crc16 is CRC-16/CCITT-FALSE (poly 0x1021, init 0xFFFF, no reflection).
func crc16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// buildFrame wraps a body in the on-air framing: type, length, body, CRC.
// The length byte lets the receiver learn how long the frame is after only two
// symbols, without needing the beacon first.
func buildFrame(kind byte, body []byte) ([]byte, error) {
	if len(body)+2 > 255 {
		return nil, fmt.Errorf("frame body of %d bytes exceeds the 253-byte limit", len(body))
	}
	out := make([]byte, 0, len(body)+4)
	out = append(out, kind, byte(len(body)+2))
	out = append(out, body...)
	sum := crc16(out)
	return append(out, byte(sum>>8), byte(sum)), nil
}

// BeaconFrame describes the transfer as a whole. It repeats periodically so a
// receiver that starts listening late can still learn K and T.
func BeaconFrame(session uint16, k, t int, gzip bool, transferSize, originalSize int, sha [32]byte) ([]byte, error) {
	body := make([]byte, 0, 47)
	body = binary.BigEndian.AppendUint16(body, session)
	body = binary.BigEndian.AppendUint16(body, uint16(k))
	body = binary.BigEndian.AppendUint16(body, uint16(t))
	flags := byte(0)
	if gzip {
		flags = 1
	}
	body = append(body, flags)
	body = binary.BigEndian.AppendUint32(body, uint32(transferSize))
	body = binary.BigEndian.AppendUint32(body, uint32(originalSize))
	body = append(body, sha[:]...)
	return buildFrame(FrameBeacon, body)
}

// DataFrame carries one fountain symbol identified by its encoding symbol id.
func DataFrame(session uint16, esi int, symbol []byte) ([]byte, error) {
	if esi < 0 || esi > 0xFFFFFF {
		return nil, fmt.Errorf("esi %d does not fit in 24 bits", esi)
	}
	body := make([]byte, 0, 5+len(symbol))
	body = binary.BigEndian.AppendUint16(body, session)
	body = append(body, byte(esi>>16), byte(esi>>8), byte(esi))
	body = append(body, symbol...)
	return buildFrame(FrameData, body)
}

// Beacon is a decoded beacon frame.
type Beacon struct {
	Session      uint16
	K, T         int
	Gzip         bool
	TransferSize int
	OriginalSize int
	SHA256       [32]byte
}

// Data is a decoded data frame.
type Data struct {
	Session uint16
	ESI     int
	Symbol  []byte
}

// ParseFrame validates a frame's CRC and returns either a Beacon or a Data.
func ParseFrame(frame []byte) (*Beacon, *Data, error) {
	if len(frame) < 4 {
		return nil, nil, errors.New("frame too short")
	}
	if int(frame[1])+2 != len(frame) {
		return nil, nil, fmt.Errorf("length byte %d disagrees with %d bytes received", frame[1], len(frame))
	}
	if crc16(frame[:len(frame)-2]) != binary.BigEndian.Uint16(frame[len(frame)-2:]) {
		return nil, nil, errors.New("crc mismatch")
	}
	body := frame[2 : len(frame)-2]
	switch frame[0] {
	case FrameBeacon:
		if len(body) != 47 {
			return nil, nil, fmt.Errorf("beacon body is %d bytes, want 47", len(body))
		}
		b := &Beacon{
			Session:      binary.BigEndian.Uint16(body[0:2]),
			K:            int(binary.BigEndian.Uint16(body[2:4])),
			T:            int(binary.BigEndian.Uint16(body[4:6])),
			Gzip:         body[6] == 1,
			TransferSize: int(binary.BigEndian.Uint32(body[7:11])),
			OriginalSize: int(binary.BigEndian.Uint32(body[11:15])),
		}
		copy(b.SHA256[:], body[15:47])
		return b, nil, nil
	case FrameData:
		if len(body) < 6 {
			return nil, nil, errors.New("data body too short")
		}
		return nil, &Data{
			Session: binary.BigEndian.Uint16(body[0:2]),
			ESI:     int(body[2])<<16 | int(body[3])<<8 | int(body[4]),
			Symbol:  append([]byte(nil), body[5:]...),
		}, nil
	default:
		return nil, nil, fmt.Errorf("unknown frame type 0x%02x", frame[0])
	}
}

// nibbles splits bytes into 4-bit symbols, high nibble first.
func nibbles(data []byte) []int {
	out := make([]int, 0, len(data)*2)
	for _, b := range data {
		out = append(out, int(b>>4), int(b&0x0F))
	}
	return out
}

// Modulate renders frames as a mono waveform. Phase is carried across symbol
// boundaries so the tone changes are continuous and produce no click; the only
// shaping needed is a short fade where a block meets silence.
func Modulate(frames [][]byte, sampleRate int, b Band, amplitude float64) ([]float32, error) {
	if b.Parallel() {
		return modulateParallel(frames, sampleRate, b, amplitude)
	}
	if err := b.Validate(sampleRate); err != nil {
		return nil, err
	}
	sr := float64(sampleRate)
	fade := int(0.001 * sr)

	out := make([]float32, int(0.2*sr)) // lead-in silence
	phase := 0.0

	emit := func(freq, dur float64) int {
		n := int(dur * sr)
		step := 2 * math.Pi * freq / sr
		for i := 0; i < n; i++ {
			out = append(out, float32(amplitude*math.Sin(phase)))
			phase += step
			if phase > 2*math.Pi {
				phase -= 2 * math.Pi
			}
		}
		return n
	}

	for _, frame := range frames {
		start := len(out)
		emit(b.SyncFreq(), b.SyncSec)
		for _, n := range nibbles(wrapFrame(frame, b.Parity)) {
			emit(b.ToneFreq(n), b.SymbolSec)
		}
		// Ramp the block's edges so the transitions to and from silence do not
		// radiate broadband click energy across the whole analysis band.
		for i := 0; i < fade && start+i < len(out); i++ {
			w := float32(0.5 * (1 - math.Cos(math.Pi*float64(i)/float64(fade))))
			out[start+i] *= w
			out[len(out)-1-i] *= w
		}
		out = append(out, make([]float32, int(b.GapSec*sr))...)
		phase = 0
	}
	return out, nil
}
