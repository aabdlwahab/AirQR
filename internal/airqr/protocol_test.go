package airqr

import (
	"bytes"
	"testing"
)

func TestTransferRoundTripShuffledWithDuplicates(t *testing.T) {
	input := bytes.Repeat([]byte("airqr moves text through light\n"), 80)
	transfer, err := NewTransfer(input, Options{ChunkSize: 50, Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(transfer.Frames) < 2 {
		t.Fatalf("expected multiple frames, got %d", len(transfer.Frames))
	}

	payloads := []string{
		transfer.Frames[1].Payload,
		transfer.Frames[0].Payload,
		transfer.Frames[1].Payload,
	}
	for _, frame := range transfer.Frames[2:] {
		payloads = append(payloads, frame.Payload)
	}

	var frames []Frame
	for _, payload := range payloads {
		frame, err := ParseFrame(payload)
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	got, err := Reassemble(frames)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, input) {
		t.Fatal("round trip mismatch")
	}
}

func TestReassembleMissingFrame(t *testing.T) {
	input := bytes.Repeat([]byte("x"), 200)
	transfer, err := NewTransfer(input, Options{ChunkSize: 40, Compress: false})
	if err != nil {
		t.Fatal(err)
	}

	_, err = Reassemble(transfer.Frames[:len(transfer.Frames)-1])
	if err == nil {
		t.Fatal("expected missing frame error")
	}
}

func TestParseFrameRejectsBadPayload(t *testing.T) {
	if _, err := ParseFrame("not-airqr"); err == nil {
		t.Fatal("expected parse error")
	}
}
