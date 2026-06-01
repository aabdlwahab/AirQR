package airqr

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// AIRQR2 is a systematic random-linear fountain code over GF(256).
//
// The transfer bytes are split into K source symbols of T bytes each (the last
// symbol zero-padded). An encoder can emit an unbounded stream of coded symbols
// identified by an encoding symbol id (ESI):
//
//   - ESI in [0, K)  -> the source symbol itself (systematic).
//   - ESI >= K       -> a random GF(256) linear combination of all source
//     symbols, with coefficients derived deterministically from the ESI.
//
// A decoder reconstructs the K source symbols from ANY K linearly independent
// coded symbols (in practice K plus a tiny overhead), regardless of which ones
// arrived or in what order. This removes the "missing frame N stalls the whole
// transfer" failure of the fixed AIRQR1 chunking: every captured frame that
// raises the rank makes progress, and the sender can stream fresh repair
// symbols forever without ever repeating.
const fountainPrefix = "AIRQR2"

// gfExp/gfLog are GF(256) exponent/log tables for the QR-standard primitive
// polynomial 0x11D with generator 2. gfExp is doubled so a+b (a,b < 255) never
// overflows, letting multiplication skip the modulo.
var (
	gfExp [512]byte
	gfLog [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

func gfInv(a byte) byte {
	// a != 0 is a caller invariant (only pivots are inverted).
	return gfExp[255-int(gfLog[a])]
}

// fountainRNG is splitmix32: a small, fully specified 32-bit PRNG. The Go uint32
// arithmetic here is mirrored bit-for-bit by the JavaScript decoder (Math.imul +
// `>>> 0`), so both ends generate identical coefficient vectors from an ESI.
type fountainRNG struct{ state uint32 }

func newFountainRNG(seed uint32) fountainRNG { return fountainRNG{state: seed} }

func (r *fountainRNG) next() uint32 {
	r.state += 0x9e3779b9
	z := r.state
	z = (z ^ (z >> 16)) * 0x21f0aaad
	z = (z ^ (z >> 15)) * 0x735a2d97
	return z ^ (z >> 15)
}

// fountainCoeffs returns the GF(256) coefficient vector (length K) for the coded
// symbol with the given ESI. Systematic symbols are unit vectors; repair symbols
// are dense and pseudo-random but deterministic, so the decoder regenerates them
// from the ESI alone — coefficients never travel in the frame.
func fountainCoeffs(esi, k int) []byte {
	coeffs := make([]byte, k)
	if esi < k {
		coeffs[esi] = 1
		return coeffs
	}
	rng := newFountainRNG(uint32(esi))
	nonzero := false
	for i := 0; i < k; i++ {
		coeffs[i] = byte(rng.next())
		if coeffs[i] != 0 {
			nonzero = true
		}
	}
	if !nonzero {
		// Vanishingly unlikely, but an all-zero row is useless; force one term.
		coeffs[esi%k] = 1
	}
	return coeffs
}

// FountainEncoder splits an input into source symbols and emits coded symbols on
// demand. It is cheap to construct and to query, so a sender can generate one
// fresh symbol per displayed animation frame.
type FountainEncoder struct {
	SessionID    string
	K            int
	T            int
	Flags        string
	TransferSize int
	OriginalSize int
	SHA256Hex    string

	source [][]byte
}

// NewFountainEncoder prepares a fountain transfer. opts.ChunkSize is the symbol
// size T; opts.Compress controls gzip, identically to NewTransfer.
func NewFountainEncoder(input []byte, opts Options) (*FountainEncoder, error) {
	if opts.ChunkSize <= 0 {
		return nil, fmt.Errorf("chunk size must be greater than 0")
	}

	shaHex, flags, transferBytes := prepareTransfer(input, opts.Compress)

	t := opts.ChunkSize
	k := (len(transferBytes) + t - 1) / t
	if k == 0 {
		k = 1
	}

	source := make([][]byte, k)
	for i := 0; i < k; i++ {
		symbol := make([]byte, t)
		start := i * t
		end := start + t
		if end > len(transferBytes) {
			end = len(transferBytes)
		}
		if start < len(transferBytes) {
			copy(symbol, transferBytes[start:end])
		}
		source[i] = symbol
	}

	return &FountainEncoder{
		SessionID:    newSessionID(),
		K:            k,
		T:            t,
		Flags:        flags,
		TransferSize: len(transferBytes),
		OriginalSize: len(input),
		SHA256Hex:    shaHex,
		source:       source,
	}, nil
}

// Symbol returns the T coded bytes for the given ESI.
func (e *FountainEncoder) Symbol(esi int) []byte {
	out := make([]byte, e.T)
	if esi < e.K {
		copy(out, e.source[esi])
		return out
	}
	coeffs := fountainCoeffs(esi, e.K)
	for i, c := range coeffs {
		if c == 0 {
			continue
		}
		src := e.source[i]
		for j := 0; j < e.T; j++ {
			out[j] ^= gfMul(c, src[j])
		}
	}
	return out
}

// Payload renders the full AIRQR2 frame string for the given ESI.
func (e *FountainEncoder) Payload(esi int) string {
	encoded := base64.RawURLEncoding.EncodeToString(e.Symbol(esi))
	return strings.Join([]string{
		fountainPrefix,
		e.SessionID,
		strconv.Itoa(esi),
		strconv.Itoa(e.K),
		strconv.Itoa(e.T),
		e.Flags,
		strconv.Itoa(e.TransferSize),
		strconv.Itoa(e.OriginalSize),
		e.SHA256Hex,
		encoded,
	}, "|")
}

// FountainFrame is a parsed AIRQR2 frame.
type FountainFrame struct {
	SessionID    string
	ESI          int
	K            int
	T            int
	Flags        string
	TransferSize int
	OriginalSize int
	SHA256Hex    string
	Data         []byte
	Payload      string
}

// IsFountainPayload reports whether a payload is an AIRQR2 frame.
func IsFountainPayload(payload string) bool {
	return strings.HasPrefix(payload, fountainPrefix+"|")
}

// ParseFountainFrame validates and parses an AIRQR2 frame payload.
func ParseFountainFrame(payload string) (FountainFrame, error) {
	parts := strings.Split(payload, "|")
	if len(parts) != 10 {
		return FountainFrame{}, fmt.Errorf("expected 10 fountain fields, got %d", len(parts))
	}
	if parts[0] != fountainPrefix {
		return FountainFrame{}, fmt.Errorf("unsupported fountain prefix %q", parts[0])
	}
	esi, err := strconv.Atoi(parts[2])
	if err != nil || esi < 0 {
		return FountainFrame{}, fmt.Errorf("invalid esi %q", parts[2])
	}
	k, err := strconv.Atoi(parts[3])
	if err != nil || k <= 0 {
		return FountainFrame{}, fmt.Errorf("invalid K %q", parts[3])
	}
	t, err := strconv.Atoi(parts[4])
	if err != nil || t <= 0 {
		return FountainFrame{}, fmt.Errorf("invalid symbol size %q", parts[4])
	}
	flags := parts[5]
	if flags != flagPlain && flags != flagGzip {
		return FountainFrame{}, fmt.Errorf("unsupported fountain flags %q", flags)
	}
	transferSize, err := strconv.Atoi(parts[6])
	if err != nil || transferSize < 0 {
		return FountainFrame{}, fmt.Errorf("invalid transfer size %q", parts[6])
	}
	originalSize, err := strconv.Atoi(parts[7])
	if err != nil || originalSize < 0 {
		return FountainFrame{}, fmt.Errorf("invalid original size %q", parts[7])
	}
	if len(parts[8]) != 64 {
		return FountainFrame{}, errors.New("invalid sha256 length")
	}
	if _, err := hex.DecodeString(parts[8]); err != nil {
		return FountainFrame{}, fmt.Errorf("invalid sha256 hex: %w", err)
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[9])
	if err != nil {
		return FountainFrame{}, fmt.Errorf("invalid symbol data: %w", err)
	}
	if len(data) != t {
		return FountainFrame{}, fmt.Errorf("symbol length %d does not match size %d", len(data), t)
	}

	return FountainFrame{
		SessionID:    parts[1],
		ESI:          esi,
		K:            k,
		T:            t,
		Flags:        flags,
		TransferSize: transferSize,
		OriginalSize: originalSize,
		SHA256Hex:    strings.ToLower(parts[8]),
		Data:         data,
		Payload:      payload,
	}, nil
}

// FountainDecoder reconstructs a transfer from coded symbols using incremental
// Gauss-Jordan elimination over GF(256). It keeps the received system in reduced
// row echelon form, so once Rank reaches K the stored data rows are already the
// source symbols.
type FountainDecoder struct {
	K            int
	T            int
	Flags        string
	TransferSize int
	OriginalSize int
	SHA256Hex    string
	SessionID    string

	coeffRows [][]byte // coeffRows[col] is the pivot row for that column, or nil
	dataRows  [][]byte
	Rank      int
}

// NewFountainDecoder allocates a decoder for K source symbols of T bytes.
func NewFountainDecoder(k, t int) *FountainDecoder {
	return &FountainDecoder{
		K:         k,
		T:         t,
		coeffRows: make([][]byte, k),
		dataRows:  make([][]byte, k),
	}
}

// Add folds one coded symbol into the system. It returns true once the decoder
// has full rank (K independent symbols). Adding a linearly dependent symbol is
// harmless and returns the current completion state.
func (d *FountainDecoder) Add(esi int, data []byte) bool {
	if len(data) != d.T {
		return d.Rank == d.K
	}
	coeffs := fountainCoeffs(esi, d.K)
	row := append([]byte(nil), data...)

	// Reduce against existing pivots (which are in RREF, so this zeroes every
	// existing pivot column in the incoming row).
	for col := 0; col < d.K; col++ {
		if coeffs[col] == 0 || d.coeffRows[col] == nil {
			continue
		}
		factor := coeffs[col]
		pc := d.coeffRows[col]
		pd := d.dataRows[col]
		for i := 0; i < d.K; i++ {
			coeffs[i] ^= gfMul(factor, pc[i])
		}
		for i := 0; i < d.T; i++ {
			row[i] ^= gfMul(factor, pd[i])
		}
	}

	pivot := -1
	for col := 0; col < d.K; col++ {
		if coeffs[col] != 0 {
			pivot = col
			break
		}
	}
	if pivot < 0 {
		return d.Rank == d.K // redundant symbol
	}

	// Normalize the new row to a leading 1.
	inv := gfInv(coeffs[pivot])
	for i := 0; i < d.K; i++ {
		coeffs[i] = gfMul(coeffs[i], inv)
	}
	for i := 0; i < d.T; i++ {
		row[i] = gfMul(row[i], inv)
	}

	// Eliminate the new pivot column from all existing pivot rows to keep RREF.
	for col := 0; col < d.K; col++ {
		if d.coeffRows[col] == nil || d.coeffRows[col][pivot] == 0 {
			continue
		}
		factor := d.coeffRows[col][pivot]
		pc := d.coeffRows[col]
		pd := d.dataRows[col]
		for i := 0; i < d.K; i++ {
			pc[i] ^= gfMul(factor, coeffs[i])
		}
		for i := 0; i < d.T; i++ {
			pd[i] ^= gfMul(factor, row[i])
		}
	}

	d.coeffRows[pivot] = coeffs
	d.dataRows[pivot] = row
	d.Rank++
	return d.Rank == d.K
}

// Result returns the decoded original input once the decoder has full rank,
// verifying transfer size, original size, and SHA-256.
func (d *FountainDecoder) Result() ([]byte, error) {
	if d.Rank != d.K {
		return nil, fmt.Errorf("decoder not complete: rank %d/%d", d.Rank, d.K)
	}
	var packed bytes.Buffer
	packed.Grow(d.K * d.T)
	for col := 0; col < d.K; col++ {
		packed.Write(d.dataRows[col])
	}
	result := packed.Bytes()
	if d.TransferSize > len(result) {
		return nil, fmt.Errorf("transfer size %d exceeds decoded bytes %d", d.TransferSize, len(result))
	}
	result = result[:d.TransferSize]
	if d.Flags == flagGzip {
		decoded, err := gunzipBytes(result)
		if err != nil {
			return nil, err
		}
		result = decoded
	}
	if len(result) != d.OriginalSize {
		return nil, fmt.Errorf("size mismatch: got %d bytes, expected %d", len(result), d.OriginalSize)
	}
	if err := verifyHash(result, d.SHA256Hex); err != nil {
		return nil, err
	}
	return result, nil
}
