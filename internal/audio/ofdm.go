package audio

import (
	"fmt"
	"math"
)

// Parallel mode: differentially-encoded QPSK across many simultaneous
// subcarriers.
//
// Serial MFSK sends one tone at a time, so it carries log2(N) bits per symbol.
// Doubling its bandwidth buys a single extra bit, which puts it within about
// 20% of its ceiling already. Sending M subcarriers at once instead carries
// 2*M bits in the same symbol time — the spectral efficiency goes from
// log2(N)/N to 2 bits/s/Hz.
//
// Symbol time is deliberately left long. A room's impulse response runs to
// tens of milliseconds, so shortening symbols to go faster would smear each
// one into the next; going parallel buys speed without touching the symbol
// time that reverberation constrains.
//
// Two choices keep the receiver simple enough to have no channel estimator:
//
//   - Every subcarrier frequency is an integer multiple of 1/SymbolSec, so the
//     cyclic prefix and suffix are exact continuations of the symbol. Early
//     reflections land in the prefix instead of in the data.
//
//   - Bits are carried in the phase *change* of each subcarrier between
//     consecutive symbols, never in its absolute phase. The measured speaker
//     response ripples by 17 dB across this band and every subcarrier arrives
//     with its own unknown phase, but each is compared only against itself one
//     symbol earlier, so the channel cancels out instead of having to be
//     measured.
func (b Band) Parallel() bool { return b.Carriers > 0 }

// CarrierFreq returns the centre frequency of subcarrier i.
func (b Band) CarrierFreq(i int) float64 { return b.Base + float64(i)*b.Spacing }

// BlockSec is the airtime of one parallel symbol: cyclic prefix, the useful
// symbol, then cyclic suffix.
func (b Band) BlockSec() float64 { return b.PrefixSec + b.SymbolSec + b.SuffixSec }

// BitsPerBlock is how many payload bits one parallel symbol carries.
func (b Band) BitsPerBlock() int { return b.PhaseBits() * b.Carriers }

// Gray coding between data values and constellation indices, so a phase error
// large enough to land in a neighbouring point costs one bit rather than
// several.
//
// dataOf[q] is the value carried by constellation index q; phaseOf is its
// inverse. For two bits the mapping happens to be its own inverse, which is no
// longer true at three, so both directions are tabulated.
var (
	dataOf  = map[int][]int{}
	phaseOf = map[int][]int{}
)

func init() {
	for _, bits := range []int{2, 3} {
		n := 1 << bits
		d := make([]int, n)
		p := make([]int, n)
		for q := 0; q < n; q++ {
			d[q] = q ^ (q >> 1)
			p[d[q]] = q
		}
		dataOf[bits], phaseOf[bits] = d, p
	}
}

// PhaseBits is how many bits each subcarrier carries per symbol: 2 for QPSK,
// 3 for 8-PSK. Zero means the original QPSK.
//
// Going from 2 to 3 is the only lever left with real leverage. The usable
// ultrasonic window on typical hardware runs about 19-21 kHz — below that sits
// a deep null, above it the reconstruction filter — so widening the band alone
// cannot do much more. Packing another bit onto every subcarrier is worth 1.5x
// across the whole band at once, at the cost of halving the angular distance
// between decision boundaries.
func (b Band) PhaseBits() int {
	if b.Phase == 0 {
		return 2
	}
	return b.Phase
}

// Phases is the size of the constellation.
func (b Band) Phases() int { return 1 << b.PhaseBits() }

// validateParallel checks the orthogonality and alignment invariants the
// receiver depends on.
func (b Band) validateParallel(sampleRate int) error {
	if b.Spacing*b.SymbolSec < 0.999 {
		return fmt.Errorf("band %s: spacing %.0f Hz is too narrow for a %.0f ms symbol (need >= %.0f Hz)",
			b.Name, b.Spacing, b.SymbolSec*1000, 1/b.SymbolSec)
	}
	// The prefix is only a true cyclic extension when every subcarrier
	// completes a whole number of cycles in one symbol.
	for i := 0; i < b.Carriers; i++ {
		cycles := b.CarrierFreq(i) * b.SymbolSec
		if math.Abs(cycles-math.Round(cycles)) > 1e-9 {
			return fmt.Errorf("band %s: subcarrier %d at %.1f Hz is not an integer multiple of %.1f Hz",
				b.Name, i, b.CarrierFreq(i), 1/b.SymbolSec)
		}
	}
	if top := b.SyncFreq(); top >= float64(sampleRate)/2 {
		return fmt.Errorf("band %s: sync tone %.0f Hz is at or above Nyquist %.0f Hz",
			b.Name, top, float64(sampleRate)/2)
	}
	return nil
}

// bitsOf expands bytes to a bit slice, most significant bit first.
func bitsOf(data []byte) []int {
	out := make([]int, 0, len(data)*8)
	for _, by := range data {
		for i := 7; i >= 0; i-- {
			out = append(out, int(by>>uint(i))&1)
		}
	}
	return out
}

// bytesOf packs bits back into bytes, discarding any trailing partial byte.
func bytesOf(bits []int) []byte {
	out := make([]byte, 0, len(bits)/8)
	for i := 0; i+8 <= len(bits); i += 8 {
		var by byte
		for j := 0; j < 8; j++ {
			by = by<<1 | byte(bits[i+j]&1)
		}
		out = append(out, by)
	}
	return out
}

// blocksForBytes is how many data symbols a frame of n bytes occupies.
func (b Band) blocksForBytes(n int) int {
	per := b.BitsPerBlock()
	return (n*8 + per - 1) / per
}

// modulateParallel renders frames as simultaneous differentially-keyed
// subcarriers. Each frame opens with a reference symbol that carries no data
// and exists only to give the first data symbol something to differ from.
func modulateParallel(frames [][]byte, sampleRate int, b Band, amplitude float64) ([]float32, error) {
	if err := b.validateParallel(sampleRate); err != nil {
		return nil, err
	}
	sr := float64(sampleRate)
	prefixN := int(b.PrefixSec * sr)
	symbolN := int(b.SymbolSec * sr)
	suffixN := int(b.SuffixSec * sr)
	blockN := prefixN + symbolN + suffixN
	taperN := prefixN
	if suffixN < taperN {
		taperN = suffixN
	}

	// Keep every subcarrier well below clipping once they add up.
	perCarrier := amplitude / math.Sqrt(float64(b.Carriers))

	out := make([]float32, int(0.2*sr))

	// emitBlock writes one parallel symbol. Sample index 0 of the block is
	// -PrefixSec into the symbol, so the prefix is generated from the same
	// sinusoids and is an exact cyclic extension.
	emitBlock := func(phases []float64) {
		start := len(out)
		for n := 0; n < blockN; n++ {
			t := float64(n-prefixN) / sr
			sum := 0.0
			for k := 0; k < b.Carriers; k++ {
				sum += math.Sin(2*math.Pi*b.CarrierFreq(k)*t + phases[k])
			}
			out = append(out, float32(perCarrier*sum))
		}
		// Taper the redundant prefix and suffix so consecutive symbols, whose
		// phases jump, do not meet at a step. A step would radiate broadband
		// click energy at the symbol rate — audible, which defeats the point of
		// an ultrasonic band.
		for i := 0; i < taperN; i++ {
			w := float32(0.5 * (1 - math.Cos(math.Pi*float64(i)/float64(taperN))))
			out[start+i] *= w
			out[start+blockN-1-i] *= w
		}
	}

	// emitTone writes the sync tone that opens each frame.
	emitTone := func(freq, dur float64) {
		start := len(out)
		n := int(dur * sr)
		phase := 0.0
		step := 2 * math.Pi * freq / sr
		for i := 0; i < n; i++ {
			out = append(out, float32(amplitude*math.Sin(phase)))
			phase += step
		}
		fade := int(0.001 * sr)
		for i := 0; i < fade && i < n; i++ {
			w := float32(0.5 * (1 - math.Cos(math.Pi*float64(i)/float64(fade))))
			out[start+i] *= w
		}
	}

	for _, frame := range frames {
		emitTone(b.SyncFreq(), b.SyncSec)

		phases := make([]float64, b.Carriers) // reference symbol: all zero
		emitBlock(phases)

		air := wrapFrame(frame, b.Parity)
		bits := bitsOf(air)
		pb := b.PhaseBits()
		step := 2 * math.Pi / float64(b.Phases())
		enc := phaseOf[pb]
		for blk := 0; blk < b.blocksForBytes(len(air)); blk++ {
			for k := 0; k < b.Carriers; k++ {
				base := pb * (blk*b.Carriers + k)
				v := 0
				for i := 0; i < pb; i++ {
					v <<= 1
					if base+i < len(bits) {
						v |= bits[base+i]
					}
				}
				// Advance this subcarrier's phase by the constellation point
				// the bits select. The receiver reads the advance, not the
				// absolute phase.
				phases[k] += float64(enc[v]) * step
			}
			emitBlock(phases)
		}
		out = append(out, make([]float32, int(b.GapSec*sr))...)
	}

	// M subcarriers at amplitude/sqrt(M) each can still add up well past full
	// scale when their phases happen to line up, which would clip on the way
	// into a 16-bit WAV. Scale the whole transmission once so the worst
	// alignment just reaches the requested amplitude.
	peak := float32(0)
	for _, v := range out {
		if v > peak {
			peak = v
		} else if -v > peak {
			peak = -v
		}
	}
	if peak > float32(amplitude) {
		scale := float32(amplitude) / peak
		for i := range out {
			out[i] *= scale
		}
	}
	return out, nil
}

// carrierPhasors returns the complex amplitude of every subcarrier over one
// useful symbol window, skipping the cyclic prefix.
func carrierPhasors(samples []float32, blockStart, prefixN, symbolN int, b Band, sr float64) []complex128 {
	out := make([]complex128, b.Carriers)
	start := blockStart + prefixN
	if start < 0 || start+symbolN > len(samples) {
		return out
	}
	for k := 0; k < b.Carriers; k++ {
		w := 2 * math.Pi * b.CarrierFreq(k) / sr
		var re, im float64
		for n := 0; n < symbolN; n++ {
			v := float64(samples[start+n])
			re += v * math.Cos(w*float64(n))
			im += v * math.Sin(w*float64(n))
		}
		out[k] = complex(re/float64(symbolN), im/float64(symbolN))
	}
	return out
}

// demodulateParallel locates each frame from its sync tone and reads the
// differential phases.
func demodulateParallel(samples []float32, sampleRate int, b Band) []Demodulated {
	sr := float64(sampleRate)
	syncN := int(b.SyncSec * sr)
	if syncN <= 0 || len(samples) < syncN {
		return nil
	}

	hop := int(0.002 * sr)
	if hop < 1 {
		hop = 1
	}
	positions := (len(samples) - syncN) / hop
	if positions < 0 {
		return nil
	}
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
	if median := medianOf(mags); median > 0 && peak < minSyncRatio*median {
		return nil
	}

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
		if f, ok := readFrameParallel(samples, c*hop+syncN, b, sr); ok {
			f.Offset = c * hop
			out = append(out, f)
		}
	}
	return out
}

// readFrameParallel reads one frame starting near dataStart, refining the block
// alignment first.
func readFrameParallel(samples []float32, dataStart int, b Band, sr float64) (Demodulated, bool) {
	prefixN := int(b.PrefixSec * sr)
	symbolN := int(b.SymbolSec * sr)
	blockN := prefixN + symbolN + int(b.SuffixSec*sr)

	// decodeAt reads count data symbols starting from a given block boundary,
	// returning the bits and how cleanly the phases landed in their quadrants.
	pb := b.PhaseBits()
	phases := b.Phases()
	phaseStep := 2 * math.Pi / float64(phases)
	dec := dataOf[pb]

	decodeAt := func(off, count int) ([]int, float64, bool) {
		if off < 0 || off+(count+1)*blockN > len(samples) {
			return nil, 0, false
		}
		prev := carrierPhasors(samples, off, prefixN, symbolN, b, sr)
		bits := make([]int, 0, count*b.BitsPerBlock())
		margin := 0.0
		for blk := 1; blk <= count; blk++ {
			cur := carrierPhasors(samples, off+blk*blockN, prefixN, symbolN, b, sr)
			for k := 0; k < b.Carriers; k++ {
				// The phase advance of this subcarrier against itself one
				// symbol earlier. Whatever the channel did to its amplitude and
				// phase divides out.
				//
				// Correlating a sin() carrier against a cos/sin pair yields a
				// phasor at (pi/2 - phi), so conj(cur)*prev is what recovers
				// +delta-phi; cur*conj(prev) would measure its negation and
				// decode every quadrant backwards.
				d := complexConj(cur[k]) * prev[k]
				angle := math.Atan2(imag(d), real(d))
				q := int(math.Round(angle/phaseStep)) & (phases - 1)
				err := math.Abs(angle - float64(q)*phaseStep)
				for err > math.Pi {
					err = 2*math.Pi - err
				}
				// Normalised against the decision boundary, which is half a
				// constellation step: 1.0 is dead centre, 0.0 is on the edge.
				margin += 1 - err/(phaseStep/2)
				v := dec[q]
				for i := pb - 1; i >= 0; i-- {
					bits = append(bits, v>>uint(i)&1)
				}
			}
			prev = cur
		}
		return bits, margin / float64(count*b.Carriers), true
	}

	// Align on the phase-decision margin, not on received energy.
	//
	// Energy hardly varies with alignment here — the tapered prefix and suffix
	// are a small fraction of each block — so an energy metric happily settles
	// half a symbol off, where the window straddles two symbols and every bit
	// is noise. How cleanly the differential phases land in their quadrants
	// collapses the moment the window slips, which is exactly the signal an
	// aligner wants.
	//
	// The search only has to cover the sync detector's own resolution plus the
	// cyclic prefix, so it stays narrow: ranging over a whole block would
	// invite locking onto the neighbouring symbol.
	search := prefixN + int(0.002*sr)
	step := prefixN / 4
	if step < 1 {
		step = 1
	}
	bestOff, bestMargin := -1, -2.0
	for off := dataStart - search; off <= dataStart+search; off += step {
		_, margin, ok := decodeAt(off, 3)
		if ok && margin > bestMargin {
			bestMargin, bestOff = margin, off
		}
	}
	if bestOff < 0 {
		return Demodulated{}, false
	}

	if b.Parity > 0 {
		head, _, ok := decodeAt(bestOff, b.blocksForBytes(3))
		if !ok {
			return Demodulated{}, false
		}
		h := bytesOf(head)
		if len(h) < 3 {
			return Demodulated{}, false
		}
		frameLen := int(majority(h[0], h[1], h[2]))
		if frameLen < 4 || frameLen > maxFrameLen(b.Parity) {
			return Demodulated{}, false
		}
		airLen := 3 + frameLen + b.Parity
		allBits, quality, ok := decodeAt(bestOff, b.blocksForBytes(airLen))
		if !ok {
			return Demodulated{}, false
		}
		air := bytesOf(allBits)
		if len(air) < airLen {
			return Demodulated{}, false
		}
		frame, _, ok := RSDecode(air[3:airLen], b.Parity)
		if !ok {
			return Demodulated{}, false
		}
		return Demodulated{Bytes: frame, Quality: quality}, true
	}

	// Two bytes give the type and length. One data symbol carries 2*Carriers
	// bits, which covers them whenever there are 8 or more subcarriers.
	headBits, _, ok := decodeAt(bestOff, b.blocksForBytes(2))
	if !ok {
		return Demodulated{}, false
	}
	head := bytesOf(headBits)
	if len(head) < 2 {
		return Demodulated{}, false
	}
	total := int(head[1]) + 2
	if total < 4 || total > 255 {
		return Demodulated{}, false
	}
	allBits, quality, ok := decodeAt(bestOff, b.blocksForBytes(total))
	if !ok {
		return Demodulated{}, false
	}
	frame := bytesOf(allBits)
	if len(frame) < total {
		return Demodulated{}, false
	}
	return Demodulated{Bytes: frame[:total], Quality: quality}, true
}

func complexConj(c complex128) complex128 { return complex(real(c), -imag(c)) }
