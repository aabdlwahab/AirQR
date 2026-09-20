package audio

// Reed-Solomon over GF(256), used to repair frames rather than discard them.
//
// The acoustic channel does not fail uniformly. Measured over the air, frame
// quality is high — differential phases land well inside their quadrants on
// almost every subcarrier — yet roughly half of all frames still failed their
// CRC, because a handful of subcarriers sit in nulls that reflections comb into
// the response and corrupt a byte or two out of seventy. Without correction a
// frame with two bad bytes is worth exactly as much as one that never arrived.
//
// Errors land in whole bytes here (a wrong quadrant on one subcarrier corrupts
// the two bits it carries), which is the shape Reed-Solomon is best at.
//
// The field is the same one the AIRQR2 fountain uses: primitive polynomial
// 0x11D, generator 2.
var (
	rsExp [512]byte
	rsLog [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		rsExp[i] = byte(x)
		rsLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		rsExp[i] = rsExp[i-255]
	}
}

func rsMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return rsExp[int(rsLog[a])+int(rsLog[b])]
}

func rsDiv(a, b byte) byte {
	if a == 0 {
		return 0
	}
	return rsExp[int(rsLog[a])-int(rsLog[b])+255]
}

func rsPow(a byte, n int) byte {
	if a == 0 {
		return 0
	}
	e := (int(rsLog[a]) * n) % 255
	if e < 0 {
		e += 255
	}
	return rsExp[e]
}

func rsInv(a byte) byte { return rsExp[255-int(rsLog[a])] }

// Polynomials below are in ASCENDING order: p[i] is the coefficient of x^i.
// Syndromes, the error locator and the error evaluator are all naturally
// ascending, and mixing conventions is how this algorithm usually goes wrong.

func rsPolyMulAsc(p, q []byte) []byte {
	out := make([]byte, len(p)+len(q)-1)
	for i, a := range p {
		if a == 0 {
			continue
		}
		for j, b := range q {
			out[i+j] ^= rsMul(a, b)
		}
	}
	return out
}

func rsPolyEvalAsc(p []byte, x byte) byte {
	y := byte(0)
	for i := len(p) - 1; i >= 0; i-- {
		y = rsMul(y, x) ^ p[i]
	}
	return y
}

// rsGenerator builds the generator polynomial for nsym parity symbols, highest
// power first, as the systematic encoder's long division wants it.
func rsGenerator(nsym int) []byte {
	g := []byte{1}
	for i := 0; i < nsym; i++ {
		// Multiply by (x - alpha^i); in this field subtraction is xor.
		next := make([]byte, len(g)+1)
		copy(next, g)
		for j, c := range g {
			next[j+1] ^= rsMul(c, rsExp[i])
		}
		g = next
	}
	return g
}

// RSEncode returns nsym parity bytes for data. The transmitted codeword is
// data followed by these parity bytes.
func RSEncode(data []byte, nsym int) []byte {
	if nsym <= 0 {
		return nil
	}
	gen := rsGenerator(nsym)
	rem := make([]byte, len(data)+nsym)
	copy(rem, data)
	for i := 0; i < len(data); i++ {
		coef := rem[i]
		if coef == 0 {
			continue
		}
		for j := 1; j < len(gen); j++ {
			rem[i+j] ^= rsMul(gen[j], coef)
		}
	}
	return rem[len(data):]
}

// RSDecode corrects up to nsym/2 byte errors in a codeword of data followed by
// nsym parity bytes, returning the corrected data, how many errors were fixed,
// and whether it succeeded.
func RSDecode(code []byte, nsym int) ([]byte, int, bool) {
	if nsym <= 0 || len(code) <= nsym || len(code) > 255 {
		return nil, 0, false
	}
	work := append([]byte(nil), code...)
	n := len(work)

	// Syndromes. Array position p holds the coefficient of x^(n-1-p), so an
	// error at p has locator X = alpha^(n-1-p).
	synd := make([]byte, nsym)
	clean := true
	for j := 0; j < nsym; j++ {
		v := byte(0)
		for _, c := range work {
			v = rsMul(v, rsExp[j]) ^ c
		}
		synd[j] = v
		if v != 0 {
			clean = false
		}
	}
	if clean {
		return work[:n-nsym], 0, true
	}

	// Berlekamp-Massey for the error locator, ascending.
	lam := []byte{1}
	b := []byte{1}
	L, m := 0, 1
	bb := byte(1)
	for i := 0; i < nsym; i++ {
		d := synd[i]
		for k := 1; k <= L && k < len(lam); k++ {
			d ^= rsMul(lam[k], synd[i-k])
		}
		switch {
		case d == 0:
			m++
		case 2*L <= i:
			t := append([]byte(nil), lam...)
			scale := rsDiv(d, bb)
			if len(lam) < len(b)+m {
				grown := make([]byte, len(b)+m)
				copy(grown, lam)
				lam = grown
			}
			for k, c := range b {
				lam[k+m] ^= rsMul(scale, c)
			}
			L, b, bb, m = i+1-L, t, d, 1
		default:
			scale := rsDiv(d, bb)
			if len(lam) < len(b)+m {
				grown := make([]byte, len(b)+m)
				copy(grown, lam)
				lam = grown
			}
			for k, c := range b {
				lam[k+m] ^= rsMul(scale, c)
			}
			m++
		}
	}
	if L <= 0 || 2*L > nsym {
		return nil, 0, false
	}
	for len(lam) > L+1 {
		lam = lam[:L+1]
	}

	// Chien search: lam(alpha^-e) == 0 marks an error whose locator is
	// alpha^e, which sits at array position n-1-e.
	var positions []int
	var locators []byte
	for e := 0; e < n; e++ {
		if rsPolyEvalAsc(lam, rsInv(rsPow(2, e))) == 0 {
			positions = append(positions, n-1-e)
			locators = append(locators, rsPow(2, e))
		}
	}
	if len(positions) != L {
		return nil, 0, false
	}

	// Forney. omega = (S * lam) truncated to nsym terms; each magnitude uses
	// the product form of the locator's derivative, which avoids having to
	// take a formal derivative in a characteristic-2 field.
	omega := rsPolyMulAsc(synd, lam)
	if len(omega) > nsym {
		omega = omega[:nsym]
	}
	for i, pos := range positions {
		xi := locators[i]
		xiInv := rsInv(xi)
		den := byte(1)
		for j, xj := range locators {
			if j != i {
				den = rsMul(den, 1^rsMul(xj, xiInv))
			}
		}
		if den == 0 {
			return nil, 0, false
		}
		work[pos] ^= rsDiv(rsPolyEvalAsc(omega, xiInv), den)
	}

	// Verify: a genuine correction zeroes every syndrome. Without this a
	// miscorrection beyond capacity would hand back confidently wrong bytes.
	for j := 0; j < nsym; j++ {
		v := byte(0)
		for _, c := range work {
			v = rsMul(v, rsExp[j]) ^ c
		}
		if v != 0 {
			return nil, 0, false
		}
	}
	return work[:n-nsym], L, true
}
