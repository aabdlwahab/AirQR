# AirQR

AirQR is a terminal tool for moving text across an air gap, as QR codes in the
terminal or as sound from the speakers.

It reads text from a file or standard input, compresses it when useful, splits it
into ordered frames, and renders one or more QR codes in the terminal. Multi-part
transfers animate until interrupted, so a protocol-aware scanner can collect the
frames in any pass.

## Usage

```sh
go run ./cmd/airqr send message.txt
go run ./cmd/airqr send < message.txt
go run ./cmd/airqr inspect message.txt
go run ./cmd/airqr decode frames.txt > message.txt
go run ./cmd/airqr audio message.txt
go run ./cmd/airqr web
```

If no file is provided, `send` reads from standard input. In an interactive
terminal, paste your text and press `Ctrl-D` to start.

Useful flags:

```sh
airqr send --fps 0.8 --chunk-size 70 message.txt
airqr send --ecc Q message.txt
airqr send --cycles 1 --no-clear message.txt
airqr send --no-compress message.txt
airqr inspect --chunk-size 90 message.txt
```

`--ecc` sets the QR error-correction level: `L`, `M` (default), `Q`, or `H`.
Higher levels survive glare, blur, and motion better when a phone photographs the
terminal, at the cost of less data per frame. Raise it to `Q` or `H` if dense
frames are hard to scan; lower it to `L` to pack the most bytes into each QR.

`decode` expects one AirQR frame payload per line. It is mostly a verifier and a
building block for the future phone scanner.

`web` serves the scanner web app:

```sh
airqr web
airqr web --addr 0.0.0.0:8747
airqr web --addr 0.0.0.0:8747 --tls-cert cert.pem --tls-key key.pem
```

Open `http://127.0.0.1:8747` in a browser, grant camera access, and scan AirQR
frames from the terminal. Browser camera APIs require a secure context: localhost
works for desktop testing, while phones usually need the app hosted over HTTPS.

To open the scanner from a phone on the same Wi-Fi, bind to all interfaces:

```sh
./airqr web --addr 0.0.0.0:8747
```

The command prints one or more `Phone URL` lines. Open the matching URL on the
phone, for example `http://192.168.1.42:8747`. The page will load over HTTP, but
iPhone camera access requires HTTPS unless the page is on localhost.

For camera scanning on an iPhone, serve HTTPS with a certificate the phone
trusts:

```sh
./airqr web --addr 0.0.0.0:8747 --tls-cert cert.pem --tls-key key.pem
```

## Sound

The same transfer can cross the gap as audio instead of QR codes. `airqr audio`
modulates the AIRQR2 fountain symbols as 16-tone MFSK and plays them through a
chosen output device, or writes a WAV:

```sh
airqr audio message.txt
airqr audio --band ultrasonic-fast message.txt
airqr audio --out transfer.wav --no-play message.txt
airqr audio --decode transfer.wav > message.txt
```

Pick the speaker with `--device`, by id or by any part of its name:

```sh
airqr audio --list-devices
airqr audio --device "MacBook Pro" message.txt
airqr audio --device 95 message.txt
```

`--list-devices` prints each device's sample rate and flags any that are too
low for the upper bands. This matters more than it sounds: a virtual output
(a loopback driver, a conferencing app) can sit at 8 or 16 kHz, and everything
above its Nyquist limit is not attenuated but *absent*. The waveform is always
generated at the chosen device's own rate so nothing resamples it on the way
out.

### Bands

Serial bands send one tone at a time (16-tone MFSK). Parallel bands send two
bits on each of many subcarriers at once, with Reed-Solomon protecting every
frame — several times faster for the same spectrum.

| Band | Spectrum | Mode | Measured over the air | Notes |
| --- | --- | --- | --- | --- |
| `fast` | 1.50–4.50 kHz | MFSK | ~98 B/s | Loudest and quickest; clearly audible |
| `audible` | 1.20–2.70 kHz | MFSK | ~52 B/s | Narrower, more forgiving |
| `ultrasonic` | 19.00–19.75 kHz | MFSK | ~27 B/s | Inaudible; the original, slowest option |
| `audible-fast` | 1.20–2.75 kHz | 32×QPSK | — | Audible, needs a short path |
| `ultrasonic-fast` | 19.00–19.75 kHz | 16×QPSK | **~101 B/s** | Inaudible, survives a reverberant room |
| `ultrasonic-wide` | 19.00–20.55 kHz | 32×QPSK | **~167 B/s** | Faster; wants a short, direct path |
| `ultrawide` | 19.00–21.15 kHz | 44×8PSK | **~258 B/s** | Very fast; a desk apart |
| `ultrawide-max` | 19.00–21.35 kHz | 48×8PSK | **~477 B/s** | Fastest there is; devices practically touching |

Figures are end to end through a MacBook Pro's speakers and microphone, on a
10 KB text file, so they include gzip, framing and parity. Measure on something
representative: on a very small transfer the fixed per-frame cost dominates and
every band looks slower than it is.

The ultrasonic bands sit at 19.0–20.6 kHz because that region measured as both
the strongest part of a MacBook Pro's speaker response and the quietest part of
the ambient spectrum — room noise up there is roughly 29 dB below the 1–4 kHz
band, where speech, fans and keyboards live. It also stays clear of a deep null
near 18.75 kHz. Note that dogs and cats hear it perfectly well.

`ultrasonic-wide` reaches to 20.55 kHz and `ultrawide` to 21.0 kHz including its
sync tone. Both are comfortable at a 48 kHz capture rate but close to the
anti-alias filter of a device recording at 44.1 kHz, so `ultrasonic-fast` is the
safer default across unknown hardware.

`ultrawide` is the end of the line, and it is worth knowing why. It carries
three bits per subcarrier instead of two, and its 8 ms cyclic prefix leaves less
guard against reflections than the 12 ms the other parallel bands use. Against
a simulated room it recovers nothing, where `ultrasonic-fast` still recovers
everything; over a short, direct path it delivers every frame. Treat it as the
setting for a phone lying next to the laptop, and fall back a step if frames
stop landing. `ultrawide-max` is the same idea pushed one notch further and
wants the devices almost in contact.

### The ceiling

Two separate limits stop this going further, and neither is the speaker.

The first is a hard wall at **21.9 kHz**. A sweep shows output holding to within
6 dB of peak at 21.8 kHz, then collapsing 27 dB by 22.0 kHz and 113 dB by
23.0 kHz. That is the 48 kHz reconstruction filter, not the driver, and nothing
below it can be recovered.

The second binds sooner and is the more interesting one: **power per
subcarrier**, not spectrum. Every extra subcarrier takes a share of one power
budget, and a higher crest factor drags the whole transmission down again when
it is normalised away from clipping. Sweeping the count against a simulated
channel shows exactly where that stops paying:

| carriers | top carrier | channel B/s | clean | near-field | desk |
| --- | --- | --- | --- | --- | --- |
| 32 | 20.55 kHz | 400 | 7/7 | 7/7 | 7/7 |
| 40 | 20.95 kHz | 500 | 7/7 | 7/7 | 5/7 |
| **44** | **21.15 kHz** | **550** | **7/7** | **7/7** | **4/7** |
| 48 | 21.35 kHz | 600 | 7/7 | 4/7 | 1/7 |
| 52 | 21.55 kHz | 650 | 3/7 | 1/7 | 0/7 |

So `ultrawide` stops at 44 rather than filling the window. Over the air, 52
subcarriers did finish 0.6 s sooner but delivered only 15 frames of 18 where 44
delivered all 18 — and it fell apart in simulation. There is spectrum left
above 21.15 kHz; there is no link budget left to put in it.

Both were then tested rather than assumed, along with every other lever, and
all of them are now closed:

**A higher output sample rate does nothing.** The 21.9 kHz wall does not move.
Driving the speakers at 96 kHz instead of 48 kHz puts the cliff in exactly the
same place: 21.5 kHz holds to within 2 dB of peak, 22.0 kHz is 49 dB down, and
everything above is *numerically zero* rather than merely quiet. 22.05 kHz is
Nyquist for 44.1 kHz, which is the tell — the built-in microphone reports a
96 kHz stream but is bandlimited internally and upsampled. The ceiling belongs
to the receiver, not to the speakers or the converter, so no sender-side rate
change can reach past it. A phone is no better.

**16-PSK does not work.** Four bits per subcarrier fails even on a clean,
attenuated path, never mind a room: the angular distance between decision
boundaries is too small at any power the speakers can deliver.
`TestSixteenPSKIsNotViable` pins that so it is not re-derived later.

**Longer symbols amortise the prefix but cost more than they return.** A
100-subcarrier QPSK band on a 40 ms symbol reaches 521 B/s with better
reverberation tolerance than `ultrawide`, but over the air it came in *slower*
(7.8 s against 7.6 s) and costs 3.3× as much to demodulate — 33 ms per second of
audio against 10 — which matters to a phone decoding live.

**A feedback channel is impossible by construction.** The receiver is on the far
side of the gap, so nothing it measures can reach the sender. That is also why
the receiver cannot usefully recommend a band: the recommendation has no way
home. The sender has to carry enough margin for the worst path it will meet,
which is what the band ladder and the Reed-Solomon parity are for.

What remained was a sweep of every sender-side combination of subcarrier count,
bits per subcarrier, symbol length and prefix. The frontier is sharp:

| bits | carriers | symbol | prefix | channel B/s | clean/near/desk | |
| --- | --- | --- | --- | --- | --- | --- |
| 3 | 48 | 20 ms | 4 ms | 692 | 7/7/0 | **`ultrawide-max`** |
| 3 | 48 | 20 ms | 8 ms | 600 | 7/7/7 | |
| 3 | 44 | 20 ms | 8 ms | 550 | 7/7/7 | **`ultrawide`** |
| 2 | 100 | 40 ms | 8 ms | 500 | 7/7/7 | slower over the air |

Those numbers moved once the reference symbol stopped starting every subcarrier
at phase zero. Aligned that way they sum into a single coherent spike, and since
the whole transmission is normalised against its peak, that spike was costing
real radiated power — 24.9 dB of crest factor at 180 subcarriers. Newman phases
spread it: crest fell to 13.3 dB and RMS rose by 6 dB at 40 subcarriers and
nearly 12 dB at 180. The receiver needed no change at all, because differential
decoding only ever looks at the *difference* between consecutive symbols and
never at the reference's absolute phases. Every band got quieter-sounding and
stronger at once, and `ultrawide-max` could afford 48 subcarriers where it
previously managed 40.

So `ultrawide-max` is the fastest configuration that still delivers, and it is
fast only because it gives up reverberation guard entirely — 4 ms of cyclic
prefix against `ultrawide`'s 8. Measured with the devices touching it delivered
every frame; against a simulated desk it recovers nothing at all.

### Why the audible band does not help

The ultrasonic window is about 2.5 kHz wide. The audible band is four times
that, the speakers are more efficient across it, and on paper it reaches several
times the rate — a 200-subcarrier band at 1–11 kHz measures 2,152 payload bytes
per second against `ultrawide-max`'s 477.

It does not survive contact with a room. Delivery collapses to roughly a third
of frames, because a band spanning 1–11 kHz crosses far more of the nulls that
reflections comb into the response than a 2.5 kHz band does, and a frame dies on
the first byte Reed-Solomon cannot repair. A narrower 1–8 kHz variant decoded
once and then failed twice on repeat runs with the same file and the same
placement. Raising redundancy to compensate gives the speed straight back.

The honest ceiling is therefore not the spectrum but frame delivery. The fix is
error correction that knows *which* subcarriers failed: the receiver already
measures a per-subcarrier phase margin on every symbol and throws it away, and
Reed-Solomon corrects twice as many erasures as errors. Marking the weakest
subcarriers as erasures rather than letting them read as wrong bytes would
roughly double the correction budget, which is what a wide band needs to hold
together. That is the next thing worth building.

### Why parallel is the only way to go faster

Serial MFSK carries `log2(N)` bits per symbol, so doubling its bandwidth buys a
single extra bit — it was already within about 20% of its ceiling. Symbol time
cannot simply be shortened either: a room's impulse response runs to tens of
milliseconds, and symbols shorter than that smear into one another.

Bandwidth runs out quickly too, and not where you would expect — see
[The ceiling](#the-ceiling). Past it the remaining lever is bits per subcarrier,
which is what `ultrawide` spends: 8-PSK instead of QPSK is worth 1.5× across
every subcarrier at once.

Sending many subcarriers simultaneously sidesteps both limits. Symbol time stays
long, so reverberation is no worse, while `2 × carriers` bits ride each symbol
instead of `log2(N)`.

Three choices keep the receiver free of any channel estimator:

- Every subcarrier is an integer multiple of `1/symbol`, so the cyclic prefix is
  an exact continuation of the symbol and early reflections land in the guard
  rather than in the data. The 12 ms prefix is sized to outlast a room's
  significant reflections; against a simulated channel a 4 ms prefix loses every
  frame once a 13 ms echo is present, while 12 ms recovers all of them.
- Bits live in each subcarrier's phase *change* between consecutive symbols,
  never its absolute phase. The speaker response ripples by 17 dB across the
  band, but each subcarrier is compared only against itself one symbol earlier,
  so the channel cancels instead of having to be measured.
- Frames carry Reed-Solomon parity. Over the air, roughly half of all frames
  failed their CRC despite high phase quality, because a few subcarriers sit in
  nulls that reflections comb into the response. Correction turned that into a
  clean decode: the same recording went from 0 beacons and 8 usable frames to
  3 beacons and 17.

The parallel bands are also *more* reverb-tolerant than the serial one, not
less. Serial MFSK has no guard interval at all, so the same echoes that the
cyclic prefix absorbs smear its symbols together.

### Receiving

The web app decodes audio at `audio.html`, linked from the scanner's settings
sheet. Press **Listen** and it decodes continuously from the microphone,
exactly as the camera scanner accumulates QR frames: it locks onto whichever
band it hears, fills the fountain rank as frames land, and finishes the moment
any K independent symbols have arrived — usually well before the sender has
finished transmitting. Dropping in a WAV decodes it in one pass instead.

The receiver disables echo cancellation, noise suppression and automatic gain
control on the capture stream. All three default to on and all three are tuned
for speech: the suppressor in particular treats a steady tone as noise and
removes it.

`airqr audio --decode` does the same job in the terminal, and tries every band
that fits under the file's sample rate unless `--band` names one.

## Frame Format

### AIRQR2 — rateless fountain (default for multi-frame transfers)

By default, multi-frame transfers use a systematic random-linear fountain code
over GF(256). The transfer bytes are split into `K` source symbols of `T` bytes;
the sender then streams an unbounded sequence of coded symbols identified by an
encoding symbol id (`esi`):

```text
AIRQR2|<session>|<esi>|<K>|<T>|<flags>|<transfer-size>|<original-size>|<sha256>|<base64url-symbol>
```

- `esi` in `[0, K)` carries the source symbol itself (systematic); `esi >= K`
  carries a pseudo-random GF(256) combination of all source symbols.
- The combination coefficients are derived deterministically from `esi` (a fully
  specified splitmix32 PRNG), so they never travel in the frame — the decoder
  regenerates them.

A receiver reconstructs the file from **any `K` linearly independent symbols**
(in practice `K` plus a frame or two), in any order. Skipped or never-seen frames
no longer matter: every frame that raises the decode rank makes progress, and the
sender emits fresh symbols forever rather than looping a fixed set. `flags`,
`original-size`, and `sha256` describe the final text after decompression;
`transfer-size` is the length of the (possibly gzipped) transfer bytes.

Pass `--fountain=false` to fall back to the AIRQR1 chunking below.

### Acoustic framing

Over the air the AIRQR2 text payload above is replaced by a binary frame. The
text form spends 104 characters of header on every frame — 64 of them the
SHA-256 hex — which a QR absorbs but which would cost seconds per frame at
acoustic rates. Instead the transfer metadata moves into a periodic beacon and
the data frames carry almost nothing:

```text
beacon:  type(1) len(1) session(2) K(2) T(2) flags(1)
         transfer-size(4) original-size(4) sha256(32) crc16(2)
data:    type(1) len(1) session(2) esi(3) symbol(T) crc16(2)
```

A beacon precedes every eighth data frame, so a receiver that starts listening
late does not wait long to learn `K` and `T`. Each block begins with its own
sync tone one slot above the top data tone, so blocks are located independently
and clock drift never accumulates across a transfer.

On the serial bands a frame carries a CRC and nothing else: it either verifies
and becomes a fountain symbol or it is discarded, which turns the channel's bit
errors into the erasures the fountain code already handles.

The parallel bands add Reed-Solomon parity, because CRC-only was not enough once
measured over the air. Each frame goes out as a triplicated length byte, the
frame, then its parity:

```text
[len][len][len] [type][len][body][crc16] [rs parity]
```

The length is sent three times to break a genuine ordering problem — the
receiver has to know how many bytes to read before it can run the correction
that would have repaired a corrupt length byte. The three copies land on
different subcarriers, so a single dead one is outvoted.

### AIRQR1 — fixed ordered chunks (legacy)

Each QR contains a full metadata header so a scanner can deduplicate, order, and
verify frames:

```text
AIRQR1|<session>|<index>|<total>|<flags>|<original-size>|<sha256>|<base64url-chunk>
```

`index` is 1-based. `flags` is `z` for gzip-compressed transfer bytes or `n`
for uncompressed transfer bytes. The SHA-256 hash and original size refer to the
final text after decompression. Every chunk is mandatory, so a single missing
frame stalls the transfer until it is seen again.

## Scanner Note

The iPhone Camera app can scan a single QR, but it will not reassemble animated
multi-frame QR transfers. Multi-frame AirQR needs a companion scanner or a
compatibility mode such as BBQr or multipart UR.

## Scanning Tips

If the web scanner captures one frame and then stalls, slow the sender down,
make each QR less dense, and raise the error-correction level:

```sh
./airqr send --fps 0.5 --chunk-size 40 --ecc Q message.txt
```

To test whether a specific frame is scannable, render it statically:

```sh
./airqr send --frame 2 --chunk-size 40 --ecc Q message.txt
```

Keep the whole QR visible inside the scanner box, avoid terminal transparency,
and press `Reset` in the web scanner before starting a new transfer. The scanner
shows the last frame seen and the next missing frames to help tune placement and
speed.

By default, animated transfers render every frame at the same QR size. This is
more reliable for phone cameras because the scan target does not resize between
frames.

The sender also uses a compact half-block terminal renderer by default so QR
codes fit in ordinary terminal widths. Use `--wide` only if the compact renderer
does not display correctly in your terminal.
