//go:build darwin

package audio

/*
#cgo LDFLAGS: -framework CoreAudio -framework AudioToolbox -framework CoreFoundation
#include <stdlib.h>
#include <string.h>
#include <CoreAudio/CoreAudio.h>
#include <AudioToolbox/AudioToolbox.h>

// The waveform lives in C memory and the render callback copies straight out
// of it, so no Go code runs on the audio thread.
typedef struct {
    float  *data;
    UInt32  frames;
    UInt32  pos;
    int     done;
} PlayCtx;

static OSStatus airqrRender(void *ref, AudioUnitRenderActionFlags *flags,
                            const AudioTimeStamp *ts, UInt32 bus,
                            UInt32 nFrames, AudioBufferList *io) {
    PlayCtx *ctx = (PlayCtx *)ref;
    for (UInt32 b = 0; b < io->mNumberBuffers; b++) {
        float *out = (float *)io->mBuffers[b].mData;
        for (UInt32 i = 0; i < nFrames; i++) {
            UInt32 p = ctx->pos + i;
            out[i] = (p < ctx->frames) ? ctx->data[p] : 0.0f;
        }
    }
    ctx->pos += nFrames;
    if (ctx->pos >= ctx->frames) ctx->done = 1;
    return noErr;
}

static AudioObjectPropertyAddress addr(AudioObjectPropertySelector sel, AudioObjectPropertyScope scope) {
    AudioObjectPropertyAddress a;
    a.mSelector = sel;
    a.mScope    = scope;
    a.mElement  = kAudioObjectPropertyElementMain;
    return a;
}

static int airqrDeviceCount(AudioDeviceID *ids, int max) {
    AudioObjectPropertyAddress a = addr(kAudioHardwarePropertyDevices, kAudioObjectPropertyScopeGlobal);
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &a, 0, NULL, &size) != noErr) return 0;
    int n = size / sizeof(AudioDeviceID);
    if (n > max) n = max;
    size = n * sizeof(AudioDeviceID);
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, 0, NULL, &size, ids) != noErr) return 0;
    return n;
}

static int airqrOutputChannels(AudioDeviceID id) {
    AudioObjectPropertyAddress a = addr(kAudioDevicePropertyStreamConfiguration, kAudioDevicePropertyScopeOutput);
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(id, &a, 0, NULL, &size) != noErr || size == 0) return 0;
    AudioBufferList *bl = (AudioBufferList *)malloc(size);
    if (!bl) return 0;
    int ch = 0;
    if (AudioObjectGetPropertyData(id, &a, 0, NULL, &size, bl) == noErr) {
        for (UInt32 i = 0; i < bl->mNumberBuffers; i++) ch += bl->mBuffers[i].mNumberChannels;
    }
    free(bl);
    return ch;
}

static int airqrDeviceName(AudioDeviceID id, char *buf, int len) {
    AudioObjectPropertyAddress a = addr(kAudioObjectPropertyName, kAudioObjectPropertyScopeGlobal);
    CFStringRef s = NULL;
    UInt32 size = sizeof(s);
    if (AudioObjectGetPropertyData(id, &a, 0, NULL, &size, &s) != noErr || !s) return 0;
    int ok = CFStringGetCString(s, buf, len, kCFStringEncodingUTF8);
    CFRelease(s);
    return ok;
}

static double airqrDeviceRate(AudioDeviceID id) {
    AudioObjectPropertyAddress a = addr(kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal);
    Float64 rate = 0;
    UInt32 size = sizeof(rate);
    if (AudioObjectGetPropertyData(id, &a, 0, NULL, &size, &rate) != noErr) return 0;
    return rate;
}

static AudioDeviceID airqrDefaultOutput(void) {
    AudioObjectPropertyAddress a = addr(kAudioHardwarePropertyDefaultOutputDevice, kAudioObjectPropertyScopeGlobal);
    AudioDeviceID id = 0;
    UInt32 size = sizeof(id);
    AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, 0, NULL, &size, &id);
    return id;
}

// airqrPlay blocks until the whole buffer has been rendered.
static int airqrPlay(AudioDeviceID dev, float *samples, int frames, double rate) {
    AudioComponentDescription desc = {0};
    desc.componentType         = kAudioUnitType_Output;
    desc.componentSubType      = kAudioUnitSubType_HALOutput;
    desc.componentManufacturer = kAudioUnitManufacturer_Apple;

    AudioComponent comp = AudioComponentFindNext(NULL, &desc);
    if (!comp) return -1;

    AudioUnit unit;
    if (AudioComponentInstanceNew(comp, &unit) != noErr) return -2;

    if (AudioUnitSetProperty(unit, kAudioOutputUnitProperty_CurrentDevice,
                             kAudioUnitScope_Global, 0, &dev, sizeof(dev)) != noErr) {
        AudioComponentInstanceDispose(unit);
        return -3;
    }

    AudioStreamBasicDescription asbd = {0};
    asbd.mSampleRate       = rate;
    asbd.mFormatID         = kAudioFormatLinearPCM;
    asbd.mFormatFlags      = kAudioFormatFlagIsFloat | kAudioFormatFlagIsPacked | kAudioFormatFlagIsNonInterleaved;
    asbd.mFramesPerPacket  = 1;
    asbd.mChannelsPerFrame = 2;
    asbd.mBitsPerChannel   = 32;
    asbd.mBytesPerPacket   = 4;
    asbd.mBytesPerFrame    = 4;
    if (AudioUnitSetProperty(unit, kAudioUnitProperty_StreamFormat,
                             kAudioUnitScope_Input, 0, &asbd, sizeof(asbd)) != noErr) {
        AudioComponentInstanceDispose(unit);
        return -4;
    }

    PlayCtx *ctx = (PlayCtx *)calloc(1, sizeof(PlayCtx));
    ctx->data   = samples;
    ctx->frames = frames;

    AURenderCallbackStruct cb;
    cb.inputProc       = airqrRender;
    cb.inputProcRefCon = ctx;
    if (AudioUnitSetProperty(unit, kAudioUnitProperty_SetRenderCallback,
                             kAudioUnitScope_Input, 0, &cb, sizeof(cb)) != noErr) {
        free(ctx);
        AudioComponentInstanceDispose(unit);
        return -5;
    }

    if (AudioUnitInitialize(unit) != noErr) { free(ctx); AudioComponentInstanceDispose(unit); return -6; }
    if (AudioOutputUnitStart(unit) != noErr) { AudioUnitUninitialize(unit); free(ctx); AudioComponentInstanceDispose(unit); return -7; }

    // Poll rather than signal: the buffer is finite and the caller is a CLI
    // that has nothing else to do while the tones play.
    while (!ctx->done) usleep(20000);
    usleep(200000); // let the device drain before tearing the unit down

    AudioOutputUnitStop(unit);
    AudioUnitUninitialize(unit);
    AudioComponentInstanceDispose(unit);
    free(ctx);
    return 0;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Device is one addressable audio output.
type Device struct {
	ID         uint32
	Name       string
	SampleRate int
	Channels   int
	Default    bool
}

// OutputDevices lists every device that can play audio, including virtual ones
// such as Background Music or a conferencing app's loopback. Those appear here
// because they are legitimate targets, but a virtual device may resample to a
// rate that destroys the band entirely, which is why the rate is shown.
func OutputDevices() ([]Device, error) {
	const max = 64
	ids := make([]C.AudioDeviceID, max)
	n := int(C.airqrDeviceCount(&ids[0], C.int(max)))
	if n == 0 {
		return nil, fmt.Errorf("no audio devices found")
	}
	def := uint32(C.airqrDefaultOutput())

	buf := (*C.char)(C.malloc(256))
	defer C.free(unsafe.Pointer(buf))

	var out []Device
	for i := 0; i < n; i++ {
		ch := int(C.airqrOutputChannels(ids[i]))
		if ch == 0 {
			continue
		}
		name := "(unnamed)"
		if C.airqrDeviceName(ids[i], buf, 256) != 0 {
			name = C.GoString(buf)
		}
		out = append(out, Device{
			ID:         uint32(ids[i]),
			Name:       name,
			SampleRate: int(C.airqrDeviceRate(ids[i])),
			Channels:   ch,
			Default:    uint32(ids[i]) == def,
		})
	}
	return out, nil
}

// DefaultOutput returns the system's current output device.
func DefaultOutput() (Device, error) {
	devs, err := OutputDevices()
	if err != nil {
		return Device{}, err
	}
	for _, d := range devs {
		if d.Default {
			return d, nil
		}
	}
	if len(devs) > 0 {
		return devs[0], nil
	}
	return Device{}, fmt.Errorf("no output device available")
}

// Play renders samples on the given device and blocks until they finish. The
// caller is expected to have generated the waveform at the device's own
// nominal rate: letting the HAL resample would put a filter right where the
// ultrasonic band lives.
func Play(samples []float32, sampleRate int, deviceID uint32) error {
	if len(samples) == 0 {
		return fmt.Errorf("nothing to play")
	}
	buf := C.malloc(C.size_t(len(samples)) * 4)
	if buf == nil {
		return fmt.Errorf("could not allocate %d samples", len(samples))
	}
	defer C.free(buf)
	copy(unsafe.Slice((*float32)(buf), len(samples)), samples)

	rc := C.airqrPlay(C.AudioDeviceID(deviceID), (*C.float)(buf), C.int(len(samples)), C.double(sampleRate))
	if rc != 0 {
		return fmt.Errorf("CoreAudio playback failed (code %d)", int(rc))
	}
	return nil
}

// PlaybackSupported reports whether this build can drive an output device.
const PlaybackSupported = true
