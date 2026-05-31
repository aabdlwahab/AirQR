package airqr

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	versionPrefix = "AIRQR1"
	flagPlain     = "n"
	flagGzip      = "z"
)

type Options struct {
	ChunkSize int
	Compress  bool
}

type Transfer struct {
	SessionID    string
	OriginalSize int
	TransferSize int
	Compressed   bool
	SHA256Hex    string
	Frames       []Frame
}

type Frame struct {
	SessionID    string
	Index        int
	Total        int
	Flags        string
	OriginalSize int
	SHA256Hex    string
	Data         []byte
	Payload      string
}

func NewTransfer(input []byte, opts Options) (Transfer, error) {
	if opts.ChunkSize <= 0 {
		return Transfer{}, fmt.Errorf("chunk size must be greater than 0")
	}

	hash := sha256.Sum256(input)
	shaHex := hex.EncodeToString(hash[:])

	flags := flagPlain
	transferBytes := input
	if opts.Compress {
		compressed, err := gzipBytes(input)
		if err != nil {
			return Transfer{}, err
		}
		if len(compressed) < len(input) {
			flags = flagGzip
			transferBytes = compressed
		}
	}

	sessionID := newSessionID()
	total := (len(transferBytes) + opts.ChunkSize - 1) / opts.ChunkSize
	if total == 0 {
		total = 1
	}

	frames := make([]Frame, 0, total)
	for i := 0; i < total; i++ {
		start := i * opts.ChunkSize
		end := start + opts.ChunkSize
		if end > len(transferBytes) {
			end = len(transferBytes)
		}
		chunk := transferBytes[start:end]
		encoded := base64.RawURLEncoding.EncodeToString(chunk)
		payload := strings.Join([]string{
			versionPrefix,
			sessionID,
			strconv.Itoa(i + 1),
			strconv.Itoa(total),
			flags,
			strconv.Itoa(len(input)),
			shaHex,
			encoded,
		}, "|")
		frames = append(frames, Frame{
			SessionID:    sessionID,
			Index:        i + 1,
			Total:        total,
			Flags:        flags,
			OriginalSize: len(input),
			SHA256Hex:    shaHex,
			Data:         append([]byte(nil), chunk...),
			Payload:      payload,
		})
	}

	return Transfer{
		SessionID:    sessionID,
		OriginalSize: len(input),
		TransferSize: len(transferBytes),
		Compressed:   flags == flagGzip,
		SHA256Hex:    shaHex,
		Frames:       frames,
	}, nil
}

func ParseFrame(payload string) (Frame, error) {
	parts := strings.Split(payload, "|")
	if len(parts) != 8 {
		return Frame{}, fmt.Errorf("expected 8 frame fields, got %d", len(parts))
	}
	if parts[0] != versionPrefix {
		return Frame{}, fmt.Errorf("unsupported frame prefix %q", parts[0])
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index <= 0 {
		return Frame{}, fmt.Errorf("invalid frame index %q", parts[2])
	}
	total, err := strconv.Atoi(parts[3])
	if err != nil || total <= 0 {
		return Frame{}, fmt.Errorf("invalid frame total %q", parts[3])
	}
	if index > total {
		return Frame{}, fmt.Errorf("frame index %d exceeds total %d", index, total)
	}
	flags := parts[4]
	if flags != flagPlain && flags != flagGzip {
		return Frame{}, fmt.Errorf("unsupported frame flags %q", flags)
	}
	originalSize, err := strconv.Atoi(parts[5])
	if err != nil || originalSize < 0 {
		return Frame{}, fmt.Errorf("invalid original size %q", parts[5])
	}
	if len(parts[6]) != sha256.Size*2 {
		return Frame{}, fmt.Errorf("invalid sha256 length")
	}
	if _, err := hex.DecodeString(parts[6]); err != nil {
		return Frame{}, fmt.Errorf("invalid sha256 hex: %w", err)
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[7])
	if err != nil {
		return Frame{}, fmt.Errorf("invalid frame data: %w", err)
	}

	return Frame{
		SessionID:    parts[1],
		Index:        index,
		Total:        total,
		Flags:        flags,
		OriginalSize: originalSize,
		SHA256Hex:    parts[6],
		Data:         data,
		Payload:      payload,
	}, nil
}

func Reassemble(frames []Frame) ([]byte, error) {
	if len(frames) == 0 {
		return nil, errors.New("no frames")
	}

	first := frames[0]
	if first.Total <= 0 {
		return nil, errors.New("invalid total")
	}
	byIndex := make([]Frame, first.Total)
	seen := make([]bool, first.Total)
	for _, frame := range frames {
		if frame.SessionID != first.SessionID {
			return nil, errors.New("mixed session ids")
		}
		if frame.Total != first.Total {
			return nil, errors.New("mixed total frame counts")
		}
		if frame.Flags != first.Flags {
			return nil, errors.New("mixed compression flags")
		}
		if frame.OriginalSize != first.OriginalSize {
			return nil, errors.New("mixed original sizes")
		}
		if frame.SHA256Hex != first.SHA256Hex {
			return nil, errors.New("mixed sha256 hashes")
		}
		if frame.Index <= 0 || frame.Index > first.Total {
			return nil, fmt.Errorf("frame index %d out of range", frame.Index)
		}
		if !seen[frame.Index-1] {
			byIndex[frame.Index-1] = frame
			seen[frame.Index-1] = true
		}
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("missing frame %d/%d", i+1, first.Total)
		}
	}

	var packed bytes.Buffer
	for _, frame := range byIndex {
		packed.Write(frame.Data)
	}
	result := packed.Bytes()
	if first.Flags == flagGzip {
		decoded, err := gunzipBytes(result)
		if err != nil {
			return nil, err
		}
		result = decoded
	}
	if len(result) != first.OriginalSize {
		return nil, fmt.Errorf("size mismatch: got %d bytes, expected %d", len(result), first.OriginalSize)
	}
	hash := sha256.Sum256(result)
	if hex.EncodeToString(hash[:]) != first.SHA256Hex {
		return nil, errors.New("sha256 mismatch")
	}
	return result, nil
}

func gzipBytes(input []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(input); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(input []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func newSessionID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
}
