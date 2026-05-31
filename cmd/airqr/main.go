package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"airqr/internal/airqr"
	"airqr/internal/qrcode"
	"airqr/internal/terminal"
)

const usage = `AirQR moves text across air gaps with terminal QR codes.

Usage:
  airqr send [flags] [file]
  airqr inspect [flags] [file]
  airqr decode [file]
  airqr web [flags]

Commands:
  send      Render one QR or animate a multi-frame transfer
  inspect   Show transfer size and frame count without rendering
  decode    Reassemble AirQR frame payloads from lines of text
  web       Serve the browser scanner app
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "send":
		err = runSend(os.Args[2:])
	case "inspect":
		err = runInspect(os.Args[2:])
	case "decode":
		err = runDecode(os.Args[2:])
	case "web":
		err = runWeb(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "airqr: %v\n", err)
		os.Exit(1)
	}
}

type commonFlags struct {
	chunkSize  int
	noCompress bool
}

func addCommonFlags(fs *flag.FlagSet, opts *commonFlags) {
	fs.IntVar(&opts.chunkSize, "chunk-size", 80, "raw transfer bytes per QR frame")
	fs.BoolVar(&opts.noCompress, "no-compress", false, "disable gzip compression")
}

func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	var common commonFlags
	var fps float64
	var cycles int
	var maxVersion int
	var noClear bool
	var monochrome bool
	var variableSize bool
	var wide bool
	var onlyFrame int
	var eccName string
	addCommonFlags(fs, &common)
	fs.Float64Var(&fps, "fps", 1.0, "animation frames per second")
	fs.IntVar(&cycles, "cycles", 0, "number of animation cycles; 0 loops until interrupted")
	fs.IntVar(&onlyFrame, "frame", 0, "render only this 1-based frame")
	fs.IntVar(&maxVersion, "max-version", 12, "maximum QR version to render")
	fs.StringVar(&eccName, "ecc", "M", "QR error-correction level: L, M, Q, or H")
	fs.BoolVar(&noClear, "no-clear", false, "do not clear the screen between animation frames")
	fs.BoolVar(&monochrome, "monochrome", false, "render with block characters instead of ANSI colors")
	fs.BoolVar(&variableSize, "variable-size", false, "render each QR at its minimum size instead of a stable fixed size")
	fs.BoolVar(&wide, "wide", false, "render legacy two-column modules instead of compact half-block QR")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fps <= 0 {
		return fmt.Errorf("--fps must be greater than 0")
	}
	if maxVersion < 1 || maxVersion > 40 {
		return fmt.Errorf("--max-version must be between 1 and 40")
	}
	if onlyFrame < 0 {
		return fmt.Errorf("--frame must be greater than or equal to 0")
	}
	level, err := qrcode.ParseEcc(eccName)
	if err != nil {
		return err
	}

	input, name, err := readInput(fs.Args())
	if err != nil {
		return err
	}

	transfer, err := airqr.NewTransfer(input, airqr.Options{
		ChunkSize: common.chunkSize,
		Compress:  !common.noCompress,
	})
	if err != nil {
		return err
	}
	if len(transfer.Frames) == 0 {
		return fmt.Errorf("internal error: no frames generated")
	}

	rendered := make([]string, len(transfer.Frames))
	versions := make([]int, len(transfer.Frames))
	fixedVersion := 1
	for i, frame := range transfer.Frames {
		code, err := qrcode.EncodeText(frame.Payload, level, maxVersion)
		if err != nil {
			return fmt.Errorf("frame %d/%d: %w; lower --chunk-size, raise --max-version, or lower --ecc", frame.Index, frame.Total, err)
		}
		if code.Version > fixedVersion {
			fixedVersion = code.Version
		}
		versions[i] = code.Version
	}
	for i, frame := range transfer.Frames {
		minVersion := 1
		if !variableSize {
			minVersion = fixedVersion
		}
		code, err := qrcode.EncodeTextAtLeast(frame.Payload, level, minVersion, maxVersion)
		if err != nil {
			return fmt.Errorf("frame %d/%d: %w; lower --chunk-size, raise --max-version, or lower --ecc", frame.Index, frame.Total, err)
		}
		rendered[i] = terminal.Render(code, terminal.RenderOptions{
			QuietZone: 4,
			Color:     !monochrome,
			Compact:   !wide,
		})
		versions[i] = code.Version
	}

	header := transferHeader(name, transfer, versions, level)
	if onlyFrame > 0 {
		if onlyFrame > len(rendered) {
			return fmt.Errorf("--frame %d exceeds transfer frame count %d", onlyFrame, len(rendered))
		}
		fmt.Print(header)
		fmt.Print(rendered[onlyFrame-1])
		fmt.Printf("Frame %d/%d\n", onlyFrame, len(rendered))
		return nil
	}
	if len(rendered) == 1 {
		fmt.Print(header)
		fmt.Print(rendered[0])
		fmt.Printf("Frame 1/1\n")
		return nil
	}

	return animate(os.Stdout, header, rendered, fps, cycles, noClear)
}

func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	var common commonFlags
	addCommonFlags(fs, &common)
	if err := fs.Parse(args); err != nil {
		return err
	}

	input, name, err := readInput(fs.Args())
	if err != nil {
		return err
	}
	transfer, err := airqr.NewTransfer(input, airqr.Options{
		ChunkSize: common.chunkSize,
		Compress:  !common.noCompress,
	})
	if err != nil {
		return err
	}
	// inspect reports size and frame count without rendering, so the EC level is
	// unused; nil versions suppress the version/EC line in the header.
	fmt.Print(transferHeader(name, transfer, nil, qrcode.LevelMedium))
	return nil
}

func runDecode(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("expected at most one file")
	}

	var input io.Reader = os.Stdin
	var file *os.File
	if len(args) == 1 {
		var err error
		file, err = os.Open(args[0])
		if err != nil {
			return err
		}
		defer file.Close()
		input = file
	}

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	var frames []airqr.Frame
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		frame, err := airqr.ParseFrame(line)
		if err != nil {
			return err
		}
		frames = append(frames, frame)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	result, err := airqr.Reassemble(frames)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(result)
	return err
}

func readInput(args []string) ([]byte, string, error) {
	if len(args) > 1 {
		return nil, "", fmt.Errorf("expected at most one file")
	}
	if len(args) == 1 {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return nil, "", err
		}
		return data, args[0], nil
	}

	info, err := os.Stdin.Stat()
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintln(os.Stderr, "Paste text, then press Ctrl-D to render QR codes.")
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, "", err
	}
	return data, "stdin", nil
}

func transferHeader(name string, transfer airqr.Transfer, versions []int, level qrcode.Ecc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "AIRQR transfer: %s\n", name)
	if transfer.Compressed {
		fmt.Fprintf(&b, "Size: %d bytes -> %d bytes gzip\n", transfer.OriginalSize, transfer.TransferSize)
	} else {
		fmt.Fprintf(&b, "Size: %d bytes\n", transfer.OriginalSize)
	}
	fmt.Fprintf(&b, "Frames: %d\n", len(transfer.Frames))
	fmt.Fprintf(&b, "Session: %s\n", transfer.SessionID)
	fmt.Fprintf(&b, "SHA-256: %s\n", transfer.SHA256Hex)
	if len(versions) > 0 {
		minVersion, maxVersion := versions[0], versions[0]
		for _, version := range versions[1:] {
			if version < minVersion {
				minVersion = version
			}
			if version > maxVersion {
				maxVersion = version
			}
		}
		if minVersion == maxVersion {
			fmt.Fprintf(&b, "QR version: %d (EC level %s)\n", minVersion, level)
		} else {
			fmt.Fprintf(&b, "QR versions: %d-%d (EC level %s)\n", minVersion, maxVersion, level)
		}
	}
	b.WriteByte('\n')
	return b.String()
}

func animate(out io.Writer, header string, frames []string, fps float64, cycles int, noClear bool) error {
	delay := time.Duration(float64(time.Second) / fps)
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)

	if !noClear {
		terminal.Clear(out)
		terminal.HideCursor(out)
		defer terminal.ShowCursor(out)
	}

	doneCycles := 0
	for {
		for i, frame := range frames {
			select {
			case <-interrupt:
				if !noClear {
					terminal.Clear(out)
				}
				return nil
			default:
			}
			content := fmt.Sprintf("%s%sFrame %d/%d  |  %.2f fps  |  Ctrl-C to quit\n", header, frame, i+1, len(frames), fps)
			if noClear {
				fmt.Fprint(out, content)
			} else {
				terminal.Paint(out, content)
			}

			timer := time.NewTimer(delay)
			select {
			case <-interrupt:
				timer.Stop()
				if !noClear {
					terminal.Clear(out)
				}
				return nil
			case <-timer.C:
			}
		}
		if cycles > 0 {
			doneCycles++
			if doneCycles >= cycles {
				return nil
			}
		}
	}
}
