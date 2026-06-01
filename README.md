# AirQR

AirQR is a terminal tool for moving text across an air gap with QR codes.

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
