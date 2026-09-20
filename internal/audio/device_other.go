//go:build !darwin

package audio

import "fmt"

// Device is one addressable audio output.
type Device struct {
	ID         uint32
	Name       string
	SampleRate int
	Channels   int
	Default    bool
}

var errUnsupported = fmt.Errorf("device playback is implemented for macOS only; write a WAV with --out and play it with any player")

// OutputDevices is unavailable off macOS.
func OutputDevices() ([]Device, error) { return nil, errUnsupported }

// DefaultOutput is unavailable off macOS.
func DefaultOutput() (Device, error) { return Device{}, errUnsupported }

// Play is unavailable off macOS.
func Play(samples []float32, sampleRate int, deviceID uint32) error { return errUnsupported }

// PlaybackSupported reports whether this build can drive an output device.
const PlaybackSupported = false
