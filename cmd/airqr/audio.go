package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"airqr/internal/airqr"
	"airqr/internal/audio"
)

// beaconEvery controls how often the transfer metadata repeats. A receiver
// cannot use any data frame until it has seen a beacon, so this bounds how
// long someone who starts listening late has to wait.
const beaconEvery = 8

func runAudio(args []string) error {
	fs := flag.NewFlagSet("audio", flag.ExitOnError)
	var (
		listDevices bool
		device      string
		out         string
		bandName    string
		symbolSize  int
		repeat      float64
		amplitude   float64
		noCompress  bool
		rate        int
		decode      bool
		noPlay      bool
	)
	fs.BoolVar(&listDevices, "list-devices", false, "list audio output devices and exit")
	fs.StringVar(&device, "device", "", "output device: numeric id or part of its name (default: system output)")
	fs.StringVar(&out, "out", "", "write the waveform to this WAV file")
	fs.StringVar(&bandName, "band", "audible", "frequency band: "+strings.Join(audio.BandNames, ", "))
	fs.IntVar(&symbolSize, "chunk-size", 64, "raw transfer bytes per audio frame")
	fs.Float64Var(&repeat, "repeat", 1.6, "fountain symbols to emit, as a multiple of K")
	fs.Float64Var(&amplitude, "amplitude", 0.5, "output amplitude, 0 to 1")
	fs.BoolVar(&noCompress, "no-compress", false, "disable gzip compression")
	fs.IntVar(&rate, "rate", 0, "sample rate for --out (default: the device rate, else 48000)")
	fs.BoolVar(&decode, "decode", false, "decode a WAV file back to the original text")
	fs.BoolVar(&noPlay, "no-play", false, "with --out, write the file without playing it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if listDevices {
		return printDevices()
	}
	bandExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "band" {
			bandExplicit = true
		}
	})
	if decode {
		return runAudioDecode(fs.Args(), bandName, bandExplicit)
	}
	return runAudioSend(fs.Args(), audioSendOpts{
		device: device, out: out, bandName: bandName, symbolSize: symbolSize,
		repeat: repeat, amplitude: amplitude, noCompress: noCompress,
		rate: rate, noPlay: noPlay,
	})
}

func printDevices() error {
	if !audio.PlaybackSupported {
		return fmt.Errorf("device listing is implemented for macOS only")
	}
	devs, err := audio.OutputDevices()
	if err != nil {
		return err
	}
	fmt.Println("Output devices:")
	for _, d := range devs {
		mark := " "
		if d.Default {
			mark = "*"
		}
		note := ""
		// A device pinned to a low rate cannot carry the upper bands at all.
		// Virtual devices (loopback drivers, conferencing apps) are the usual
		// culprit, and the failure is otherwise silent.
		if d.SampleRate > 0 && d.SampleRate < 44100 {
			note = fmt.Sprintf("  <- %d Hz: too low for anything above %.1f kHz",
				d.SampleRate, float64(d.SampleRate)/2000)
		}
		fmt.Printf("  %s %-5d %-36s %6d Hz  %dch%s\n", mark, d.ID, d.Name, d.SampleRate, d.Channels, note)
	}
	fmt.Println("\n  * = system default.  Select with --device <id> or --device <name fragment>.")
	return nil
}

func resolveDevice(spec string) (audio.Device, error) {
	if spec == "" {
		return audio.DefaultOutput()
	}
	devs, err := audio.OutputDevices()
	if err != nil {
		return audio.Device{}, err
	}
	if id, err := strconv.Atoi(spec); err == nil {
		for _, d := range devs {
			if int(d.ID) == id {
				return d, nil
			}
		}
		return audio.Device{}, fmt.Errorf("no output device with id %d (try --list-devices)", id)
	}
	var matches []audio.Device
	for _, d := range devs {
		if strings.Contains(strings.ToLower(d.Name), strings.ToLower(spec)) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 0:
		return audio.Device{}, fmt.Errorf("no output device matching %q (try --list-devices)", spec)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, len(matches))
		for i, d := range matches {
			names[i] = fmt.Sprintf("%d %q", d.ID, d.Name)
		}
		return audio.Device{}, fmt.Errorf("%q matches several devices: %s", spec, strings.Join(names, ", "))
	}
}

type audioSendOpts struct {
	device, out, bandName string
	symbolSize            int
	repeat, amplitude     float64
	noCompress, noPlay    bool
	rate                  int
}

func runAudioSend(args []string, o audioSendOpts) error {
	band, ok := audio.Bands[o.bandName]
	if !ok {
		return fmt.Errorf("unknown band %q (choose from %s)", o.bandName, strings.Join(audio.BandNames, ", "))
	}
	if o.amplitude <= 0 || o.amplitude > 1 {
		return fmt.Errorf("--amplitude must be between 0 and 1")
	}
	if o.repeat < 1 {
		return fmt.Errorf("--repeat must be at least 1")
	}

	play := !o.noPlay
	if play && !audio.PlaybackSupported {
		if o.out == "" {
			return fmt.Errorf("playback is implemented for macOS only; use --out to write a WAV instead")
		}
		play = false
	}

	var dev audio.Device
	sampleRate := o.rate
	if play {
		var err error
		if dev, err = resolveDevice(o.device); err != nil {
			return err
		}
		// Generate at the device's own rate. Letting CoreAudio resample would
		// put a conversion filter exactly where the upper bands live.
		sampleRate = dev.SampleRate
	}
	if sampleRate == 0 {
		sampleRate = 48000
	}
	if err := band.Validate(sampleRate); err != nil {
		if play {
			return fmt.Errorf("%w\n  device %q runs at %d Hz; pick another device with --device, or use --band %s",
				err, dev.Name, dev.SampleRate, lowestBand(sampleRate))
		}
		return err
	}

	input, name, err := readInput(args)
	if err != nil {
		return err
	}
	enc, err := airqr.NewFountainEncoder(input, airqr.Options{
		ChunkSize: o.symbolSize,
		Compress:  !o.noCompress,
	})
	if err != nil {
		return err
	}

	frames, err := buildAudioFrames(enc, o.repeat)
	if err != nil {
		return err
	}
	wave, err := audio.Modulate(frames, sampleRate, band, o.amplitude)
	if err != nil {
		return err
	}

	dur := float64(len(wave)) / float64(sampleRate)
	fmt.Fprintf(os.Stderr, "AirQR audio: %s\n", name)
	fmt.Fprintf(os.Stderr, "  %d bytes -> K=%d symbols of %d bytes, %d frames\n",
		enc.OriginalSize, enc.K, enc.T, len(frames))
	// A parallel band's span is set by its subcarrier count, not by the 16-tone
	// serial alphabet, and its symbol carries a cyclic prefix as well.
	if band.Parallel() {
		fmt.Fprintf(os.Stderr, "  band %s: %d subcarriers %.0f-%.0f Hz, sync %.0f Hz, %d bits each, %.0f ms/symbol\n",
			band.Name, band.Carriers, band.CarrierFreq(0), band.CarrierFreq(band.Carriers-1),
			band.SyncFreq(), band.PhaseBits(), band.BlockSec()*1000)
	} else {
		fmt.Fprintf(os.Stderr, "  band %s: tones %.0f-%.0f Hz, sync %.0f Hz, %.0f ms/symbol\n",
			band.Name, band.ToneFreq(0), band.ToneFreq(audio.Tones-1), band.SyncFreq(), band.SymbolSec*1000)
	}
	fmt.Fprintf(os.Stderr, "  %.1fs at %d Hz (%.1f payload bytes/s)\n",
		dur, sampleRate, float64(enc.OriginalSize)/dur)

	if o.out != "" {
		f, err := os.Create(o.out)
		if err != nil {
			return err
		}
		if err := audio.WriteWAV(f, wave, sampleRate); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "  wrote %s\n", o.out)
	}
	if play {
		fmt.Fprintf(os.Stderr, "  playing on %q (%d Hz)...\n", dev.Name, dev.SampleRate)
		if err := audio.Play(wave, sampleRate, dev.ID); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "  done")
	}
	return nil
}

// lowestBand names a band that still fits under the given sample rate, for the
// error message when the chosen device cannot carry the requested one.
func lowestBand(sampleRate int) string {
	best, bestTop := "", math.Inf(1)
	for _, n := range audio.BandNames {
		b := audio.Bands[n]
		if b.Validate(sampleRate) == nil && b.SyncFreq() < bestTop {
			best, bestTop = n, b.SyncFreq()
		}
	}
	if best == "" {
		return "(none fit)"
	}
	return best
}

func buildAudioFrames(enc *airqr.FountainEncoder, repeat float64) ([][]byte, error) {
	sha, err := hex.DecodeString(enc.SHA256Hex)
	if err != nil || len(sha) != 32 {
		return nil, fmt.Errorf("unexpected transfer hash %q", enc.SHA256Hex)
	}
	var sha32 [32]byte
	copy(sha32[:], sha)

	// The AIRQR2 session id is 8 hex characters; the audio framing only needs
	// enough of it to tell two concurrent transfers apart.
	sessionBytes, err := hex.DecodeString(enc.SessionID)
	if err != nil || len(sessionBytes) < 2 {
		return nil, fmt.Errorf("unexpected session id %q", enc.SessionID)
	}
	session := uint16(sessionBytes[0])<<8 | uint16(sessionBytes[1])

	beacon, err := audio.BeaconFrame(session, enc.K, enc.T, enc.Flags == "z",
		enc.TransferSize, enc.OriginalSize, sha32)
	if err != nil {
		return nil, err
	}

	// Emit more symbols than K. The fountain needs any K independent frames, so
	// the surplus is what absorbs frames lost to noise rather than a retransmit
	// request, which an acoustic one-way link has no way to make.
	total := int(math.Ceil(float64(enc.K)*repeat)) + 2
	frames := make([][]byte, 0, total+total/beaconEvery+1)
	for esi := 0; esi < total; esi++ {
		if esi%beaconEvery == 0 {
			frames = append(frames, beacon)
		}
		f, err := audio.DataFrame(session, esi, enc.Symbol(esi))
		if err != nil {
			return nil, err
		}
		frames = append(frames, f)
	}
	return frames, nil
}

func runAudioDecode(args []string, bandName string, bandExplicit bool) error {
	if len(args) != 1 {
		return fmt.Errorf("expected exactly one WAV file to decode")
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	samples, rate, err := audio.ReadWAV(raw)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "AirQR audio decode: %s (%.1fs at %d Hz)\n",
		args[0], float64(len(samples))/float64(rate), rate)

	candidates := audio.BandNames
	if bandExplicit {
		candidates = []string{bandName}
	}

	type attempt struct {
		name    string
		beacons []*audio.Beacon
		data    []*audio.Data
	}
	var best attempt
	for _, name := range candidates {
		band, ok := audio.Bands[name]
		if !ok {
			return fmt.Errorf("unknown band %q", name)
		}
		if band.Validate(rate) != nil {
			continue
		}
		var a attempt
		a.name = name
		for _, d := range audio.Demodulate(samples, rate, band) {
			b, dat, err := audio.ParseFrame(d.Bytes)
			if err != nil {
				continue
			}
			if b != nil {
				a.beacons = append(a.beacons, b)
			}
			if dat != nil {
				a.data = append(a.data, dat)
			}
		}
		fmt.Fprintf(os.Stderr, "  band %-11s %d beacons, %d data frames\n", name, len(a.beacons), len(a.data))
		if len(a.beacons)+len(a.data) > len(best.beacons)+len(best.data) {
			best = a
		}
	}
	if len(best.beacons) == 0 {
		return fmt.Errorf("no beacon frame recovered; the transfer metadata never arrived")
	}

	b := best.beacons[0]
	dec := airqr.NewFountainDecoder(b.K, b.T)
	dec.TransferSize = b.TransferSize
	dec.OriginalSize = b.OriginalSize
	dec.SHA256Hex = hex.EncodeToString(b.SHA256[:])
	dec.Flags = "n"
	if b.Gzip {
		dec.Flags = "z" // matches the AIRQR2 gzip flag
	}

	// Feed the lowest ESIs first: the systematic symbols are guaranteed
	// independent, so they raise rank fastest.
	sort.Slice(best.data, func(i, j int) bool { return best.data[i].ESI < best.data[j].ESI })
	for _, d := range best.data {
		if d.Session != b.Session {
			continue
		}
		if dec.Add(d.ESI, d.Symbol) {
			result, err := dec.Result()
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "  decoded via %s: rank %d/%d, sha256 verified\n", best.name, dec.Rank, dec.K)
			_, err = os.Stdout.Write(result)
			return err
		}
	}
	return fmt.Errorf("incomplete transfer: rank %d/%d from %d usable frames", dec.Rank, dec.K, len(best.data))
}
