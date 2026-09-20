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
| `ultrawide` | 19.00–20.95 kHz | 40×8PSK | **~255 B/s** | Fastest; short, direct path only |

Figures are end to end through a MacBook Pro's speakers and microphone on a
1,957-byte text file, so they include gzip, framing and parity.

The ultrasonic bands sit at 19.0–20.6 kHz because that region measured as both
the strongest part of a MacBook Pro's speaker response and the quietest part of
the ambient spectrum — room noise up there is roughly 29 dB below the 1–4 kHz
band, where speech, fans and keyboards live. It also stays clear of a deep null
near 18.75 kHz. Note that dogs and cats hear it perfectly well.

`ultrasonic-wide` reaches to 20.55 kHz and `ultrawide` to 21.0 kHz including its
sync tone. Both are comfortable at a 48 kHz capture rate but close to the
anti-alias filter of a device recording at 44.1 kHz, so `ultrasonic-fast` is the
safer default across unknown hardware.

`ultrawide` takes what is left of the band. It fills the whole clean stretch of
the measured response — below 19 kHz sits a deep null, above 21 kHz the 48 kHz
reconstruction filter — and carries three bits per subcarrier instead of two.
It buys that speed by spending margin twice: 8-PSK halves the angular distance
between decision boundaries, and its 8 ms cyclic prefix leaves less guard
against reflections than the 12 ms the other parallel bands use. Against a
simulated room it recovers nothing, where `ultrasonic-fast` still recovers
everything; over a short, direct path it delivered every frame. Treat it as the
setting for a phone lying next to the laptop, and fall back a step if frames
stop landing.

### Why parallel is the only way to go faster

Serial MFSK carries `log2(N)` bits per symbol, so doubling its bandwidth buys a
single extra bit — it was already within about 20% of its ceiling. Symbol time
cannot simply be shortened either: a room's impulse response runs to tens of
milliseconds, and symbols shorter than that smear into one another.

Bandwidth runs out quickly too. The clean stretch of the measured response is
roughly 19–21 kHz, so going from 32 subcarriers to 40 is only worth about 1.25×.
Past that the remaining lever is bits per subcarrier, which is what `ultrawide`
spends: 8-PSK instead of QPSK is worth 1.5× across every subcarrier at once.

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
