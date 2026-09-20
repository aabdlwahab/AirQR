package audio

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestRSCorrectsUpToCapacity(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for _, nsym := range []int{8, 16, 32} {
		capacity := nsym / 2
		for _, dataLen := range []int{16, 73, 120, 255 - nsym} {
			data := make([]byte, dataLen)
			rng.Read(data)
			code := append(append([]byte(nil), data...), RSEncode(data, nsym)...)

			for errs := 0; errs <= capacity; errs++ {
				dirty := append([]byte(nil), code...)
				used := map[int]bool{}
				for i := 0; i < errs; i++ {
					var p int
					for {
						p = rng.Intn(len(dirty))
						if !used[p] {
							break
						}
					}
					used[p] = true
					dirty[p] ^= byte(1 + rng.Intn(255))
				}
				got, n, ok := RSDecode(dirty, nsym)
				if !ok {
					t.Fatalf("nsym=%d len=%d errs=%d: decode failed", nsym, dataLen, errs)
				}
				if n != errs {
					t.Errorf("nsym=%d len=%d: reported %d errors, injected %d", nsym, dataLen, n, errs)
				}
				if !bytes.Equal(got, data) {
					t.Fatalf("nsym=%d len=%d errs=%d: wrong data recovered", nsym, dataLen, errs)
				}
			}
		}
	}
}

// Beyond capacity the decoder must report failure rather than hand back
// confidently wrong bytes: a silent miscorrection would defeat the CRC behind
// it and corrupt the transfer.
func TestRSRefusesBeyondCapacity(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	nsym := 16
	miscorrected, refused := 0, 0
	for trial := 0; trial < 300; trial++ {
		data := make([]byte, 73)
		rng.Read(data)
		code := append(append([]byte(nil), data...), RSEncode(data, nsym)...)
		dirty := append([]byte(nil), code...)
		used := map[int]bool{}
		for i := 0; i < nsym/2+3; i++ {
			var p int
			for {
				p = rng.Intn(len(dirty))
				if !used[p] {
					break
				}
			}
			used[p] = true
			dirty[p] ^= byte(1 + rng.Intn(255))
		}
		got, _, ok := RSDecode(dirty, nsym)
		if !ok {
			refused++
		} else if !bytes.Equal(got, data) {
			miscorrected++
		}
	}
	t.Logf("beyond capacity: %d refused, %d silently miscorrected of 300", refused, miscorrected)
	// A stray miscorrection is mathematically possible; the CRC behind it is
	// the backstop. It must be rare, not routine.
	if miscorrected > 6 {
		t.Errorf("%d silent miscorrections of 300 is too many", miscorrected)
	}
}

func TestRSCleanCodewordIsUntouched(t *testing.T) {
	data := []byte("AIRQR acoustic frame payload, unmodified in flight.")
	code := append(append([]byte(nil), data...), RSEncode(data, 16)...)
	got, n, ok := RSDecode(code, 16)
	if !ok || n != 0 || !bytes.Equal(got, data) {
		t.Fatalf("clean codeword: ok=%v errors=%d", ok, n)
	}
}

// A burst hitting consecutive bytes is the realistic failure here: one dead
// subcarrier corrupts the same bit positions in every symbol.
func TestRSCorrectsBurst(t *testing.T) {
	data := make([]byte, 73)
	rand.New(rand.NewSource(2)).Read(data)
	code := append(append([]byte(nil), data...), RSEncode(data, 16)...)
	dirty := append([]byte(nil), code...)
	for i := 20; i < 28; i++ {
		dirty[i] ^= 0xFF
	}
	got, n, ok := RSDecode(dirty, 16)
	if !ok || !bytes.Equal(got, data) {
		t.Fatalf("burst of 8: ok=%v errors=%d", ok, n)
	}
}
