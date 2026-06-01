package airqr

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeFrames(t *testing.T, payloads []string) ([]byte, error) {
	t.Helper()
	var dec *FountainDecoder
	for _, p := range payloads {
		frame, err := ParseFountainFrame(p)
		if err != nil {
			t.Fatalf("parse %q: %v", p, err)
		}
		if dec == nil {
			dec = NewFountainDecoder(frame.K, frame.T)
			dec.Flags = frame.Flags
			dec.TransferSize = frame.TransferSize
			dec.OriginalSize = frame.OriginalSize
			dec.SHA256Hex = frame.SHA256Hex
			dec.SessionID = frame.SessionID
		}
		if dec.Add(frame.ESI, frame.Data) {
			return dec.Result()
		}
	}
	if dec == nil {
		return nil, fmt.Errorf("no frames")
	}
	return nil, fmt.Errorf("incomplete: rank %d/%d", dec.Rank, dec.K)
}

func TestFountainRoundTripExact(t *testing.T) {
	inputs := [][]byte{
		[]byte(""),
		[]byte("hello"),
		bytes.Repeat([]byte("The quick brown fox. "), 200),
		[]byte(strings.Repeat("\x00\x01\x02\xff\xfe", 500)),
	}
	for _, compress := range []bool{true, false} {
		for ci, input := range inputs {
			enc, err := NewFountainEncoder(input, Options{ChunkSize: 40, Compress: compress})
			if err != nil {
				t.Fatalf("encoder: %v", err)
			}
			// Capture exactly the K systematic symbols.
			payloads := make([]string, enc.K)
			for esi := 0; esi < enc.K; esi++ {
				payloads[esi] = enc.Payload(esi)
			}
			got, err := decodeFrames(t, payloads)
			if err != nil {
				t.Fatalf("case %d compress=%v: %v", ci, compress, err)
			}
			if !bytes.Equal(got, input) {
				t.Fatalf("case %d compress=%v: round-trip mismatch", ci, compress)
			}
		}
	}
}

func TestFountainErasureRecovery(t *testing.T) {
	input := bytes.Repeat([]byte("air-gapped payload data; "), 400)
	enc, err := NewFountainEncoder(input, Options{ChunkSize: 64, Compress: false})
	if err != nil {
		t.Fatal(err)
	}
	if enc.K < 10 {
		t.Fatalf("expected a multi-symbol transfer, got K=%d", enc.K)
	}

	// Drop a quarter of the systematic symbols (including the troublesome
	// "frame 6") and substitute repair symbols, delivered out of order.
	dropped := map[int]bool{0: true, 5: true, 6: true, 9: true}
	for esi := 0; esi < enc.K; esi += 7 {
		dropped[esi] = true
	}
	var payloads []string
	for esi := 0; esi < enc.K; esi++ {
		if !dropped[esi] {
			payloads = append(payloads, enc.Payload(esi))
		}
	}
	// Append generous repair symbols; the decoder should stop as soon as it
	// reaches full rank regardless of which ones it used.
	for esi := enc.K; esi < enc.K+len(dropped)+16; esi++ {
		payloads = append(payloads, enc.Payload(esi))
	}
	// Shuffle deterministically (reverse) to prove order independence.
	for i, j := 0, len(payloads)-1; i < j; i, j = i+1, j-1 {
		payloads[i], payloads[j] = payloads[j], payloads[i]
	}

	got, err := decodeFrames(t, payloads)
	if err != nil {
		t.Fatalf("erasure recovery: %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Fatal("erasure recovery: payload mismatch")
	}
}

func TestFountainRepairOnly(t *testing.T) {
	// The decoder must reconstruct from repair symbols alone (no systematic),
	// proving the dense random rows are full-rank with small overhead.
	input := bytes.Repeat([]byte("repair-only "), 300)
	enc, err := NewFountainEncoder(input, Options{ChunkSize: 48, Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	var payloads []string
	for esi := enc.K; esi < enc.K*3; esi++ { // start past the systematic range
		payloads = append(payloads, enc.Payload(esi))
	}
	got, err := decodeFrames(t, payloads)
	if err != nil {
		t.Fatalf("repair-only: %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Fatal("repair-only: payload mismatch")
	}
}

func TestFountainCoeffsSystematic(t *testing.T) {
	coeffs := fountainCoeffs(3, 8)
	for i, c := range coeffs {
		want := byte(0)
		if i == 3 {
			want = 1
		}
		if c != want {
			t.Fatalf("systematic coeffs[%d] = %d, want %d", i, c, want)
		}
	}
}

// TestFountainDetectsCorruption ensures a flipped symbol byte fails verification
// rather than silently returning wrong data.
func TestFountainDetectsCorruption(t *testing.T) {
	input := []byte(strings.Repeat("integrity ", 100))
	enc, err := NewFountainEncoder(input, Options{ChunkSize: 32, Compress: false})
	if err != nil {
		t.Fatal(err)
	}
	dec := NewFountainDecoder(enc.K, enc.T)
	dec.Flags = enc.Flags
	dec.TransferSize = enc.TransferSize
	dec.OriginalSize = enc.OriginalSize
	dec.SHA256Hex = enc.SHA256Hex
	for esi := 0; esi < enc.K; esi++ {
		sym := enc.Symbol(esi)
		if esi == 2 {
			sym[0] ^= 0xff // corrupt one systematic symbol
		}
		dec.Add(esi, sym)
	}
	if _, err := dec.Result(); err == nil {
		t.Fatal("expected corruption to fail verification")
	}
}

// TestFountainWriteInteropFixture writes a fixture consumed by the JavaScript
// decoder test (web/fountain.test.mjs) so the Go encoder and JS decoder are
// proven to interoperate bit-for-bit. Regenerate by running this test.
func TestFountainWriteInteropFixture(t *testing.T) {
	input := []byte("AirQR fountain interop fixture — Gauss-Jordan over GF(256).\n")
	for i := 0; i < 90; i++ {
		input = append(input, []byte(fmt.Sprintf("line %03d: unicode ✓ and raw bytes \x00\x01\x7f follow here.\n", i))...)
	}
	// Compression off so K stays large and the solver mixes many systematic and
	// repair rows rather than collapsing to a couple of symbols.
	enc, err := NewFountainEncoder(input, Options{ChunkSize: 50, Compress: false})
	if err != nil {
		t.Fatal(err)
	}
	if enc.K < 40 {
		t.Fatalf("interop fixture should be large; got K=%d", enc.K)
	}

	// Erase a spread of systematic symbols and backfill with repair symbols.
	dropped := map[int]bool{1: true, 4: true, 7: true, 8: true, 15: true, 23: true, 31: true}
	var frames []string
	for esi := 0; esi < enc.K; esi++ {
		if !dropped[esi] {
			frames = append(frames, enc.Payload(esi))
		}
	}
	for esi := enc.K; esi < enc.K+len(dropped)+8; esi++ {
		frames = append(frames, enc.Payload(esi))
	}
	for i, j := 0, len(frames)-1; i < j; i, j = i+1, j-1 {
		frames[i], frames[j] = frames[j], frames[i]
	}

	// Sanity-check that this fixture decodes in Go before writing it.
	got, err := decodeFrames(t, frames)
	if err != nil || !bytes.Equal(got, input) {
		t.Fatalf("fixture does not decode in Go: err=%v match=%v", err, bytes.Equal(got, input))
	}

	fixture := map[string]any{
		"k":              enc.K,
		"t":              enc.T,
		"originalBase64": base64.StdEncoding.EncodeToString(input),
		"frames":         frames,
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "fountain.interop.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}
