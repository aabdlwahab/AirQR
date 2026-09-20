package audio

import (
	"bytes"
	"math"
	"math/rand"
	"testing"
)

func testFrames(t *testing.T, count, symbolSize int) [][]byte {
	t.Helper()
	var sha [32]byte
	for i := range sha {
		sha[i] = byte(i * 7)
	}
	beacon, err := BeaconFrame(0xBEEF, count, symbolSize, true, count*symbolSize, 1234, sha)
	if err != nil {
		t.Fatal(err)
	}
	frames := [][]byte{beacon}
	for i := 0; i < count; i++ {
		sym := make([]byte, symbolSize)
		for j := range sym {
			sym[j] = byte(i*31 + j)
		}
		f, err := DataFrame(0xBEEF, i, sym)
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
	return frames
}

func TestRoundTripAllBands(t *testing.T) {
	for _, name := range BandNames {
		band := Bands[name]
		t.Run(name, func(t *testing.T) {
			frames := testFrames(t, 4, 64)
			wave, err := Modulate(frames, 48000, band, 0.5)
			if err != nil {
				t.Fatal(err)
			}
			got := Demodulate(wave, 48000, band)
			if len(got) != len(frames) {
				t.Fatalf("recovered %d frames, sent %d", len(got), len(frames))
			}
			for i, g := range got {
				if !bytes.Equal(g.Bytes, frames[i]) {
					t.Errorf("frame %d mismatch", i)
				}
				if _, _, err := ParseFrame(g.Bytes); err != nil {
					t.Errorf("frame %d failed to parse: %v", i, err)
				}
			}
			dur := float64(len(wave)) / 48000
			payload := float64(len(frames)-1) * 64
			t.Logf("%s: %.2fs for %.0f payload bytes = %.1f B/s", name, dur, payload, payload/dur)
		})
	}
}

func TestRoundTripWithNoiseAndGain(t *testing.T) {
	band := Bands["ultrasonic"]
	frames := testFrames(t, 6, 64)
	wave, err := Modulate(frames, 48000, band, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	// Attenuate hard and add white noise, roughly modelling a receiver a
	// metre away in a quiet room.
	rng := rand.New(rand.NewSource(1))
	dirty := make([]float32, len(wave))
	for i, s := range wave {
		dirty[i] = float32(float64(s)*0.05 + rng.NormFloat64()*0.004)
	}
	got := Demodulate(dirty, 48000, band)
	ok := 0
	for _, g := range got {
		if _, _, err := ParseFrame(g.Bytes); err == nil {
			ok++
		}
	}
	if ok < len(frames) {
		t.Fatalf("recovered %d/%d frames through noise", ok, len(frames))
	}
}

func TestRoundTripSurvivesResampleAndClockSkew(t *testing.T) {
	band := Bands["audible"]
	frames := testFrames(t, 4, 64)
	wave, err := Modulate(frames, 48000, band, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	// Resample by a non-integer ratio to imitate a recorder whose clock runs
	// 200 ppm fast — far worse than any real pair of sound cards.
	ratio := 1.0002
	n := int(float64(len(wave)) / ratio)
	skewed := make([]float32, n)
	for i := range skewed {
		src := float64(i) * ratio
		j := int(src)
		if j+1 >= len(wave) {
			break
		}
		frac := src - float64(j)
		skewed[i] = float32(float64(wave[j])*(1-frac) + float64(wave[j+1])*frac)
	}
	got := Demodulate(skewed, 48000, band)
	ok := 0
	for _, g := range got {
		if _, _, err := ParseFrame(g.Bytes); err == nil {
			ok++
		}
	}
	if ok < len(frames) {
		t.Fatalf("recovered %d/%d frames with clock skew", ok, len(frames))
	}
}

func TestBandsAreOrthogonalAndBelowNyquist(t *testing.T) {
	for _, name := range BandNames {
		if err := Bands[name].Validate(48000); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCRCDetectsCorruption(t *testing.T) {
	f, err := DataFrame(1, 2, bytes.Repeat([]byte{0xAA}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseFrame(f); err != nil {
		t.Fatalf("clean frame rejected: %v", err)
	}
	for i := 2; i < len(f)-2; i++ {
		bad := append([]byte(nil), f...)
		bad[i] ^= 0x01
		if _, _, err := ParseFrame(bad); err == nil {
			t.Fatalf("corruption at byte %d went undetected", i)
		}
	}
}

func TestWAVRoundTrip(t *testing.T) {
	in := make([]float32, 1000)
	for i := range in {
		in[i] = float32(math.Sin(float64(i) * 0.1))
	}
	var buf bytes.Buffer
	if err := WriteWAV(&buf, in, 48000); err != nil {
		t.Fatal(err)
	}
	out, rate, err := ReadWAV(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if rate != 48000 || len(out) != len(in) {
		t.Fatalf("got %d samples at %d Hz", len(out), rate)
	}
	for i := range in {
		if math.Abs(float64(in[i]-out[i])) > 0.001 {
			t.Fatalf("sample %d: %f vs %f", i, in[i], out[i])
		}
	}
}

// echoChannel models an acoustic path: a dominant direct arrival plus discrete
// reflections, then attenuation and noise. The reflections matter twice over —
// they are what makes the frequency response ripple, and any arriving later
// than the cyclic prefix smear one symbol into the next.
func echoChannel(in []float32, sampleRate int, echoes [][2]float64, gain, noise float64, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float32, len(in))
	for i, s := range in {
		out[i] += float32(float64(s) * gain)
		for _, e := range echoes {
			d := int(e[0] * float64(sampleRate))
			if i+d < len(out) {
				out[i+d] += float32(float64(s) * gain * e[1])
			}
		}
	}
	for i := range out {
		out[i] += float32(rng.NormFloat64() * noise)
	}
	return out
}

// Channels used to characterise the bands. Delays are seconds, amplitudes are
// relative to the direct arrival.
var (
	nearFieldEchoes = [][2]float64{{0.002, 0.40}}
	roomEchoes      = [][2]float64{{0.003, 0.40}, {0.007, 0.25}, {0.013, 0.15}}
)

func TestParallelBandsSurviveEchoes(t *testing.T) {
	cases := []struct {
		band    string
		echoes  [][2]float64
		channel string
	}{
		// 16 subcarriers in 800 Hz clears a reverberant room outright. Its
		// cyclic prefix swallows the reflections, which is more than the serial
		// band manages: serial MFSK has no guard interval, so the same 13 ms
		// echo smears its symbols together and it loses most frames.
		{"ultrasonic-fast", roomEchoes, "room"},
		// 32 subcarriers spread over 1600 Hz cross more of the nulls that
		// echoes comb into the response, and a single dead subcarrier fails the
		// frame's CRC. They need a short, direct path until the frames carry
		// error correction of their own.
		{"ultrasonic-wide", nearFieldEchoes, "near-field"},
		{"audible-fast", nearFieldEchoes, "near-field"},
		// ultrawide-max spends its reverberation guard on speed, so near-field
		// is the most it is expected to hold. Anything more is a bonus, but a
		// regression below this means the 4 ms prefix has stopped working.
		{"ultrawide-max", nearFieldEchoes, "near-field"},
	}

	for _, c := range cases {
		band := Bands[c.band]
		t.Run(c.band, func(t *testing.T) {
			frames := testFrames(t, 6, 64)
			wave, err := Modulate(frames, 48000, band, 0.5)
			if err != nil {
				t.Fatal(err)
			}
			dirty := echoChannel(wave, 48000, c.echoes, 0.05, 0.0015, 3)
			ok := 0
			for _, g := range Demodulate(dirty, 48000, band) {
				if _, _, err := ParseFrame(g.Bytes); err == nil {
					ok++
				}
			}
			t.Logf("%s over a %s channel at 26 dB attenuation: %d/%d frames, %.0f B/s",
				c.band, c.channel, ok, len(frames),
				float64(band.BitsPerBlock())/band.BlockSec()/8)
			if ok != len(frames) {
				t.Errorf("%d of %d frames survived", ok, len(frames))
			}
		})
	}
}

// The parallel band should beat the serial one on reverb, not merely tie it.
// If this ever regresses, the cyclic prefix has stopped doing its job.
func TestParallelBeatsSerialInReverb(t *testing.T) {
	count := func(name string) int {
		band := Bands[name]
		frames := testFrames(t, 6, 64)
		wave, err := Modulate(frames, 48000, band, 0.5)
		if err != nil {
			t.Fatal(err)
		}
		dirty := echoChannel(wave, 48000, roomEchoes, 0.05, 0.0015, 3)
		ok := 0
		for _, g := range Demodulate(dirty, 48000, band) {
			if _, _, err := ParseFrame(g.Bytes); err == nil {
				ok++
			}
		}
		return ok
	}
	serial, parallel := count("ultrasonic"), count("ultrasonic-fast")
	t.Logf("room reverb: serial recovered %d frames, parallel recovered %d", serial, parallel)
	if parallel <= serial {
		t.Errorf("parallel (%d) should beat serial (%d) in reverb", parallel, serial)
	}
}

// 16-PSK was measured and rejected: four bits per subcarrier leaves too little
// angular margin to survive even a clean channel at usable power. This pins
// that conclusion, so nobody re-derives it by shipping a broken band.
func TestSixteenPSKIsNotViable(t *testing.T) {
	band := Bands["ultrawide"]
	band.Phase = 4
	frames := testFrames(t, 6, 64)
	wave, err := Modulate(frames, 48000, band, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	// A clean path, only attenuated — no echoes at all.
	dirty := echoChannel(wave, 48000, nil, 0.05, 0.0015, 3)
	ok := 0
	for _, g := range Demodulate(dirty, 48000, band) {
		if _, _, err := ParseFrame(g.Bytes); err == nil {
			ok++
		}
	}
	t.Logf("16-PSK on a clean, attenuated path: %d/%d frames", ok, len(frames))
	if ok == len(frames) {
		t.Errorf("16-PSK delivered everything; if that is now reliable, revisit the band table")
	}
}
