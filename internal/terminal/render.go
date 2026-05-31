package terminal

import (
	"fmt"
	"io"
	"strings"

	"airqr/internal/qrcode"
)

type RenderOptions struct {
	QuietZone int
	Color     bool
	Compact   bool
}

func Render(code *qrcode.Code, opts RenderOptions) string {
	quiet := opts.QuietZone
	if quiet < 0 {
		quiet = 0
	}
	if opts.Compact {
		return renderCompact(code, quiet, opts.Color)
	}

	size := code.Size + quiet*2
	var b strings.Builder
	for y := 0; y < size; y++ {
		if opts.Color {
			writeColorRow(&b, code, y-quiet, quiet)
		} else {
			writeMonoRow(&b, code, y-quiet, quiet)
		}
		b.WriteByte('\n')
	}
	if opts.Color {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func Clear(w io.Writer) {
	fmt.Fprint(w, "\x1b[2J\x1b[H")
}

func HideCursor(w io.Writer) {
	io.WriteString(w, "\x1b[?25l")
}

func ShowCursor(w io.Writer) {
	io.WriteString(w, "\x1b[?25h")
}

// Paint writes one animation frame as a single synchronized update so terminals
// supporting DEC mode 2026 never display a partially drawn QR. The cursor is
// moved home and stale lines below the content are erased rather than clearing
// the whole screen, which removes the blank flash between frames.
func Paint(w io.Writer, content string) {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	b.WriteString("\x1b[H")
	b.WriteString(content)
	b.WriteString("\x1b[0J")
	b.WriteString("\x1b[?2026l")
	io.WriteString(w, b.String())
}

func writeColorRow(b *strings.Builder, code *qrcode.Code, moduleY int, quiet int) {
	current := ""
	for x := -quiet; x < code.Size+quiet; x++ {
		black := moduleY >= 0 && moduleY < code.Size && x >= 0 && x < code.Size && code.Module(x, moduleY)
		next := "\x1b[47m"
		if black {
			next = "\x1b[40m"
		}
		if next != current {
			b.WriteString(next)
			current = next
		}
		b.WriteString("  ")
	}
	b.WriteString("\x1b[0m")
}

func writeMonoRow(b *strings.Builder, code *qrcode.Code, moduleY int, quiet int) {
	for x := -quiet; x < code.Size+quiet; x++ {
		black := moduleY >= 0 && moduleY < code.Size && x >= 0 && x < code.Size && code.Module(x, moduleY)
		if black {
			b.WriteString("██")
		} else {
			b.WriteString("  ")
		}
	}
}

func renderCompact(code *qrcode.Code, quiet int, color bool) string {
	size := code.Size + quiet*2
	var b strings.Builder
	for y := 0; y < size; y += 2 {
		for x := 0; x < size; x++ {
			upper := compactModule(code, x-quiet, y-quiet)
			lower := compactModule(code, x-quiet, y+1-quiet)
			if color {
				writeColorHalfBlock(&b, upper, lower)
			} else {
				writeMonoHalfBlock(&b, upper, lower)
			}
		}
		if color {
			b.WriteString("\x1b[0m")
		}
		b.WriteByte('\n')
	}
	if color {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func compactModule(code *qrcode.Code, x int, y int) bool {
	return y >= 0 && y < code.Size && x >= 0 && x < code.Size && code.Module(x, y)
}

func writeColorHalfBlock(b *strings.Builder, upperBlack bool, lowerBlack bool) {
	switch {
	case upperBlack && lowerBlack:
		b.WriteString("\x1b[40m ")
	case !upperBlack && !lowerBlack:
		b.WriteString("\x1b[47m ")
	case upperBlack && !lowerBlack:
		b.WriteString("\x1b[30;47m▀")
	case !upperBlack && lowerBlack:
		b.WriteString("\x1b[37;40m▀")
	}
}

func writeMonoHalfBlock(b *strings.Builder, upperBlack bool, lowerBlack bool) {
	switch {
	case upperBlack && lowerBlack:
		b.WriteString("█")
	case !upperBlack && !lowerBlack:
		b.WriteString(" ")
	case upperBlack && !lowerBlack:
		b.WriteString("▀")
	case !upperBlack && lowerBlack:
		b.WriteString("▄")
	}
}
