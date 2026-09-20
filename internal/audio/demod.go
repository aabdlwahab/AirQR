package audio

import (
	"math"
	"sort"
)

// goertzel returns the magnitude of one frequency over a window, normalised so
// a full-amplitude tone reads about 1.0. It costs one multiply-add per sample
// per frequency, which is what makes scanning 16 tones per symbol cheap enough
// to do at several candidate alignments.
func goertzel(samples []float32, start, n int, freq, sr float64) float64 {
	if start < 0 || start+n > len(samples) || n <= 0 {
		return 0
	}
	w := 2 * math.Pi * freq / sr
	c := 2 * math.Cos(w)
	var s1, s2 float64
	for i := 0; i < n; i++ {
		s0 := float64(samples[start+i]) + c*s1 - s2
		s2, s1 = s1, s0
	}
	power := s1*s1 + s2*s2 - c*s1*s2
	if power < 0 {
		power = 0
	}
	return 2 * math.Sqrt(power) / float64(n)
}

// symbolAt decides one symbol and reports how clearly it won. The margin —
// strongest tone over runner-up — is the signal used to choose between
// candidate timing alignments; a correct alignment separates the tones much
// more sharply than one that straddles a symbol boundary.
func symbolAt(samples []float32, start, n int, b Band, sr float64) (tone int, margin float64) {
	best, second, bestIdx := 0.0, 0.0, 0
	for i := 0; i < Tones; i++ {
		m := goertzel(samples, start, n, b.ToneFreq(i), sr)
		if m > best {
			second, best, bestIdx = best, m, i
		} else if m > second {
			second = m
		}
	}
	if second <= 0 {
		return bestIdx, math.Inf(1)
	}
	return bestIdx, best / second
}

// minSyncRatio is how far the strongest sync tone must rise above the band's
// noise floor for the band to be considered at all. Measured separation is
// roughly 65x for a band carrying a transfer against 3x for an empty one, so
// the exact value matters far less than sitting between them.
const minSyncRatio = 8.0

// medianOf returns the median of a copy of the values, leaving the input
// untouched.
func medianOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2]
}

// syncTrack returns the magnitude of one frequency over a sliding window, at
// every hop across the whole signal.
//
// Running a Goertzel at each position would cost one pass over the window per
// position — for a half-minute recording that is tens of millions of
// multiply-adds per band, enough to stall a phone for many seconds. Mixing the
// signal down once and taking prefix sums gives the identical quantity (a DFT
// bin over the window) for the same total work as a single pass, and each
// window position then costs two subtractions.
func syncTrack(samples []float32, winN, hop, positions int, freq, sr float64) []float64 {
	n := len(samples)
	sumI := make([]float64, n+1)
	sumQ := make([]float64, n+1)
	w := 2 * math.Pi * freq / sr
	for i := 0; i < n; i++ {
		phase := w * float64(i)
		v := float64(samples[i])
		sumI[i+1] = sumI[i] + v*math.Cos(phase)
		sumQ[i+1] = sumQ[i] + v*math.Sin(phase)
	}
	out := make([]float64, positions+1)
	scale := 2 / float64(winN)
	for i := 0; i <= positions; i++ {
		s := i * hop
		re := sumI[s+winN] - sumI[s]
		im := sumQ[s+winN] - sumQ[s]
		out[i] = scale * math.Hypot(re, im)
	}
	return out
}

// Demodulated is one frame recovered from the waveform.
type Demodulated struct {
	Offset  int     // sample index of the block's sync tone
	Bytes   []byte  // raw frame, CRC already verified by the caller
	Quality float64 // mean tone margin across the frame
}

// Demodulate finds every block in the waveform and returns the frames it could
// read. Each block is located independently from its own sync tone, so a
// corrupt or missing block never shifts the ones after it.
func Demodulate(samples []float32, sampleRate int, b Band) []Demodulated {
	sr := float64(sampleRate)
	syncN := int(b.SyncSec * sr)
	symN := int(b.SymbolSec * sr)
	if syncN <= 0 || symN <= 0 || len(samples) < syncN {
		return nil
	}

	// Sweep the sync frequency across the recording. The sync tone is one slot
	// above the top data tone, so nothing in the payload can produce a peak here.
	hop := int(0.002 * sr)
	if hop < 1 {
		hop = 1
	}
	positions := (len(samples) - syncN) / hop
	mags := syncTrack(samples, syncN, hop, positions, b.SyncFreq(), sr)
	peak := 0.0
	for _, m := range mags {
		if m > peak {
			peak = m
		}
	}
	if peak <= 0 {
		return nil
	}

	// Reject the band outright unless some sync tone stands well clear of the
	// band's own noise floor. The sync tone occupies a couple of percent of the
	// recording, so the median of the track is that floor.
	//
	// Without this test a band carrying no signal still yields candidates,
	// because the threshold below is a fraction of that band's own peak — 30%
	// of noise is still noise. Each one then costs a full frame read before the
	// CRC rejects it, and scanning a half-minute recording for a band that
	// isn't there costs more than decoding the band that is.
	if median := medianOf(mags); median > 0 && peak < minSyncRatio*median {
		return nil
	}

	// Keep local maxima that clear a fraction of the strongest sync seen. The
	// exclusion window is one sync length, which is enough to collapse the
	// plateau around a single tone into one candidate.
	threshold := 0.30 * peak
	guard := syncN / hop
	if guard < 1 {
		guard = 1
	}
	var candidates []int
	for i := 0; i <= positions; i++ {
		if mags[i] < threshold {
			continue
		}
		isPeak := true
		for j := i - guard; j <= i+guard; j++ {
			if j >= 0 && j <= positions && mags[j] > mags[i] {
				isPeak = false
				break
			}
		}
		if isPeak && (len(candidates) == 0 || i-candidates[len(candidates)-1] > guard) {
			candidates = append(candidates, i)
		}
	}

	var out []Demodulated
	for _, c := range candidates {
		if f, ok := readFrame(samples, c*hop+syncN, symN, b, sr); ok {
			f.Offset = c * hop
			out = append(out, f)
		}
	}
	return out
}

// readFrame decodes one frame starting near dataStart, first refining the
// timing. Clock offset between the playing and recording devices, plus the
// 2 ms resolution of the sync sweep, can leave the grid off by a useful
// fraction of a symbol; searching a half symbol either way recovers it.
func readFrame(samples []float32, dataStart, symN int, b Band, sr float64) (Demodulated, bool) {
	search := symN / 2
	step := symN / 16
	if step < 1 {
		step = 1
	}
	bestOff, bestScore := dataStart, -1.0
	for off := dataStart - search; off <= dataStart+search; off += step {
		if off < 0 {
			continue
		}
		score := 0.0
		probes := 0
		for i := 0; i < 6; i++ {
			if off+(i+1)*symN > len(samples) {
				break
			}
			_, m := symbolAt(samples, off+i*symN, symN, b, sr)
			if math.IsInf(m, 1) {
				m = 100
			}
			score += m
			probes++
		}
		if probes > 0 && score/float64(probes) > bestScore {
			bestScore, bestOff = score/float64(probes), off
		}
	}
	if bestScore < 0 {
		return Demodulated{}, false
	}

	read := func(count int) ([]byte, float64, bool) {
		total := 0.0
		bytesOut := make([]byte, 0, count)
		for i := 0; i < count; i++ {
			hi, m1 := symbolAt(samples, bestOff+(2*i)*symN, symN, b, sr)
			lo, m2 := symbolAt(samples, bestOff+(2*i+1)*symN, symN, b, sr)
			if bestOff+(2*i+2)*symN > len(samples) {
				return nil, 0, false
			}
			if !math.IsInf(m1, 1) {
				total += m1
			}
			if !math.IsInf(m2, 1) {
				total += m2
			}
			bytesOut = append(bytesOut, byte(hi<<4|lo))
		}
		return bytesOut, total / float64(2*count), true
	}

	// Two bytes give the type and the length, so the rest of the frame can be
	// read without knowing anything about the transfer yet.
	head, _, ok := read(2)
	if !ok {
		return Demodulated{}, false
	}
	total := int(head[1]) + 2
	if total < 4 || total > 255 {
		return Demodulated{}, false
	}
	frame, quality, ok := read(total)
	if !ok {
		return Demodulated{}, false
	}
	return Demodulated{Bytes: frame, Quality: quality}, true
}
