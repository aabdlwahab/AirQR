package qrcode

import (
	"bytes"
	"testing"
)

func TestEncodeVersionOneCapacity(t *testing.T) {
	code, err := EncodeBytes(bytes.Repeat([]byte("a"), 17), LevelLow, 40)
	if err != nil {
		t.Fatal(err)
	}
	if code.Version != 1 {
		t.Fatalf("expected version 1, got %d", code.Version)
	}
	if code.Size != 21 {
		t.Fatalf("expected size 21, got %d", code.Size)
	}

	code, err = EncodeBytes(bytes.Repeat([]byte("a"), 18), LevelLow, 40)
	if err != nil {
		t.Fatal(err)
	}
	if code.Version != 2 {
		t.Fatalf("expected version 2, got %d", code.Version)
	}
}

func TestHigherEccLevelLowersCapacity(t *testing.T) {
	// 17 bytes fit version 1 at level L but not at level M, so the encoder must
	// step up to version 2 once the stronger error correction is requested.
	code, err := EncodeBytes(bytes.Repeat([]byte("a"), 17), LevelMedium, 40)
	if err != nil {
		t.Fatal(err)
	}
	if code.Version != 2 {
		t.Fatalf("expected version 2 at level M, got %d", code.Version)
	}
}

func TestEncodeRespectsMaxVersion(t *testing.T) {
	_, err := EncodeBytes(bytes.Repeat([]byte("a"), 18), LevelLow, 1)
	if err == nil {
		t.Fatal("expected capacity error")
	}
}

func TestEncodeAtLeastUsesRequestedMinimumVersion(t *testing.T) {
	code, err := EncodeBytesAtLeast([]byte("hello"), LevelLow, 5, 40)
	if err != nil {
		t.Fatal(err)
	}
	if code.Version != 5 {
		t.Fatalf("expected version 5, got %d", code.Version)
	}
	if code.Size != 37 {
		t.Fatalf("expected size 37, got %d", code.Size)
	}
}

func TestParseEccRejectsUnknownLevel(t *testing.T) {
	if _, err := ParseEcc("X"); err == nil {
		t.Fatal("expected parse error for unknown level")
	}
	for name, want := range map[string]Ecc{"l": LevelLow, "M": LevelMedium, "q": LevelQuartile, "H": LevelHigh} {
		got, err := ParseEcc(name)
		if err != nil {
			t.Fatalf("ParseEcc(%q): %v", name, err)
		}
		if got != want {
			t.Fatalf("ParseEcc(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFinderPatternCorners(t *testing.T) {
	code, err := EncodeText("hello", LevelMedium, 40)
	if err != nil {
		t.Fatal(err)
	}

	points := [][2]int{
		{0, 0}, {6, 0}, {0, 6}, {6, 6},
		{code.Size - 7, 0}, {code.Size - 1, 0},
		{0, code.Size - 7}, {0, code.Size - 1},
	}
	for _, point := range points {
		if !code.Module(point[0], point[1]) {
			t.Fatalf("expected finder module at %v to be black", point)
		}
	}
}

// TestVersion7AlignmentPatternsOnTimingLine guards the alignment patterns whose
// centers fall on the timing line (positions 6,22 and 22,6 at version 7). These
// were previously skipped, which made every version >= 7 QR undecodable.
func TestVersion7AlignmentPatternsOnTimingLine(t *testing.T) {
	code, err := EncodeBytesAtLeast([]byte("alignment-pattern-regression"), LevelLow, 7, 7)
	if err != nil {
		t.Fatal(err)
	}
	if code.Version != 7 {
		t.Fatalf("expected version 7, got %d", code.Version)
	}

	// An alignment pattern is a 5x5 block that is dark everywhere except the ring
	// at Chebyshev distance 1. Verify the full block at both centers that sit on
	// the timing line, so the timing/data modules left behind by the old skip
	// logic cannot coincidentally satisfy the check.
	for _, center := range [][2]int{{6, 22}, {22, 6}} {
		cx, cy := center[0], center[1]
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				dist := dx
				if dist < 0 {
					dist = -dist
				}
				if ady := abs(dy); ady > dist {
					dist = ady
				}
				want := dist != 1
				if code.Module(cx+dx, cy+dy) != want {
					t.Fatalf("alignment module at (%d,%d) near center %v: got %v, want %v",
						cx+dx, cy+dy, center, code.Module(cx+dx, cy+dy), want)
				}
			}
		}
	}
}
