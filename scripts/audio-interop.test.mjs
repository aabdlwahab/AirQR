// Cross-language interop test for the acoustic transport: modulate a transfer
// with the Go encoder (`airqr audio --out`), then demodulate the resulting WAV
// with the browser receiver in docs/audio.js and reassemble it through the
// browser fountain decoder.
//
// This is the test that matters for the audio path. The two ends have to agree
// on tone frequencies, symbol timing, framing, CRC and byte order; any
// disagreement recovers nothing, and nothing else in the suite would catch it.
//
// Run from the repo root: node scripts/audio-interop.test.mjs
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { gunzipSync } from "node:zlib";
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { tmpdir } from "node:os";

const require = createRequire(import.meta.url);
const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const audio = require(join(root, "docs/audio.js"));
const { Decoder } = require(join(root, "docs/fountain.js"));

let failures = 0;
function check(name, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"}  ${name}${detail ? "  — " + detail : ""}`);
  if (!ok) failures++;
}

// Minimal 16-bit PCM WAV reader. The browser uses AudioContext.decodeAudioData
// instead; this keeps the test free of any browser shim.
function readWav(buf) {
  if (buf.toString("ascii", 0, 4) !== "RIFF" || buf.toString("ascii", 8, 12) !== "WAVE") {
    throw new Error("not a WAVE file");
  }
  let pos = 12;
  let channels = 0;
  let rate = 0;
  let bits = 0;
  while (pos + 8 <= buf.length) {
    const id = buf.toString("ascii", pos, pos + 4);
    const size = buf.readUInt32LE(pos + 4);
    const body = pos + 8;
    if (id === "fmt ") {
      channels = buf.readUInt16LE(body + 2);
      rate = buf.readUInt32LE(body + 4);
      bits = buf.readUInt16LE(body + 14);
    } else if (id === "data") {
      if (bits !== 16) throw new Error(`expected 16-bit PCM, got ${bits}`);
      const frames = Math.floor(size / (2 * channels));
      const out = new Float32Array(frames);
      for (let i = 0; i < frames; i++) {
        let sum = 0;
        for (let c = 0; c < channels; c++) sum += buf.readInt16LE(body + (i * channels + c) * 2) / 32768;
        out[i] = sum / channels;
      }
      return { samples: out, sampleRate: rate };
    }
    pos = body + size + (size % 2);
  }
  throw new Error("no data chunk");
}

const dir = mkdtempSync(join(tmpdir(), "airqr-audio-"));
try {
  const message = Buffer.from(
    "AirQR acoustic interop fixture.\n\n" +
      "Sphinx of black quartz, judge my vow. Pack my box with five dozen liquor jugs.\n" +
      "The transfer is gzipped, split into GF(256) fountain symbols, and carried as\n" +
      "16-tone MFSK. Any K independent frames reconstruct it, in any order.\n",
  );
  const src = join(dir, "message.txt");
  writeFileSync(src, message);

  for (const band of audio.BandNames) {
    const wav = join(dir, `${band}.wav`);
    execFileSync("go", ["run", "./cmd/airqr", "audio", "--band", band, "--out", wav, "--no-play", src], {
      cwd: root,
      stdio: ["ignore", "ignore", "ignore"],
    });

    const { samples, sampleRate } = readWav(readFileSync(wav));
    const { best, tried } = audio.decode(samples, sampleRate);

    check(`${band}: band auto-detected`, best && best.band === band, best ? `picked ${best.band}` : "nothing decoded");
    if (!best || best.band !== band) {
      console.log("      tried:", JSON.stringify(tried));
      continue;
    }
    check(`${band}: beacon recovered`, best.beacons.length > 0, `${best.beacons.length} beacons`);
    if (best.beacons.length === 0) continue;

    const b = best.beacons[0];
    const dec = new Decoder(b.k, b.t);
    // Lowest ESIs first: the systematic symbols are guaranteed independent.
    const frames = best.data.filter((d) => d.session === b.session).sort((x, y) => x.esi - y.esi);
    let done = false;
    let used = 0;
    for (const f of frames) {
      used++;
      if (dec.add(f.esi, f.symbol)) {
        done = true;
        break;
      }
    }
    check(`${band}: fountain reached full rank`, done, `rank ${dec.rank}/${b.k} from ${used} of ${frames.length} frames`);
    if (!done) continue;

    let out = Buffer.from(dec.packed().subarray(0, b.transferSize));
    if (b.gzip) out = gunzipSync(out);
    check(`${band}: original size`, out.length === b.originalSize, `${out.length} vs ${b.originalSize}`);

    const sha = createHash("sha256").update(out).digest();
    check(`${band}: sha256 matches beacon`, sha.equals(Buffer.from(b.sha256)));
    check(`${band}: bytes identical to source`, out.equals(message));
  }

  // The live path is what the page actually uses at a microphone: audio arrives
  // in small chunks and the transfer has to complete while it streams, without
  // the receiver ever holding the whole session in memory.
  for (const band of ["fast", "ultrasonic", "ultrasonic-fast", "ultrasonic-wide", "ultrawide"]) {
    const wav = join(dir, `${band}.wav`);
    const { samples, sampleRate } = readWav(readFileSync(wav));
    const rx = new audio.LiveReceiver(sampleRate, { scanSeconds: 0.4 });

    const chunk = Math.round(sampleRate * 0.128); // a typical mic callback
    let peakRetained = 0;
    let completedAt = null;
    let dec = null;
    for (let at = 0; at < samples.length; at += chunk) {
      rx.push(samples.subarray(at, Math.min(at + chunk, samples.length)));
      peakRetained = Math.max(peakRetained, rx.len);
      if (!rx.beacon || completedAt) continue;
      if (!dec) dec = new Decoder(rx.beacon.k, rx.beacon.t);
      for (const f of rx.frames.splice(0)) {
        if (f.session === rx.beacon.session && dec.add(f.esi, f.symbol)) {
          completedAt = at / sampleRate;
          break;
        }
      }
    }

    check(`${band}: live stream completed`, completedAt !== null,
      completedAt !== null ? `at ${completedAt.toFixed(1)}s of ${(samples.length / sampleRate).toFixed(1)}s` : "never reached full rank");
    if (completedAt === null) continue;

    check(`${band}: live locked onto the right band`, rx.band === band, `locked ${rx.band}`);

    // The window must stay bounded, otherwise a receiver left listening gets
    // steadily slower exactly when it needs to keep up. The buffer is trimmed
    // on the scans, not on every chunk, so it can carry one scan interval of
    // history plus another that has accumulated since — bounded either way, and
    // independent of how long the stream runs.
    const scanSamples = Math.round(sampleRate * 0.4);
    const bound = audio.maxFrameSamples(audio.Bands[band], sampleRate, 80) + 2 * scanSamples + chunk;
    check(`${band}: live window stayed bounded`, peakRetained <= bound,
      `${(peakRetained / sampleRate).toFixed(1)}s retained over a ${(samples.length / sampleRate).toFixed(0)}s stream, bound ${(bound / sampleRate).toFixed(1)}s`);

    let out = Buffer.from(dec.packed().subarray(0, rx.beacon.transferSize));
    if (rx.beacon.gzip) out = gunzipSync(out);
    check(`${band}: live bytes identical to source`, out.equals(message));
  }

  // A receiver on a voice-optimised capture path gets a low sample rate, which
  // removes the ultrasonic band entirely. That has to be reported, not guessed at.
  const lowRate = audio.bandFits(audio.Bands.ultrasonic, 16000);
  check("ultrasonic band rejected at 16 kHz", lowRate === false);
  check("audible band accepted at 16 kHz", audio.bandFits(audio.Bands.audible, 16000) === true);
} finally {
  rmSync(dir, { recursive: true, force: true });
}

console.log(failures === 0 ? "\nall audio interop checks passed" : `\n${failures} check(s) failed`);
process.exit(failures === 0 ? 0 : 1);
