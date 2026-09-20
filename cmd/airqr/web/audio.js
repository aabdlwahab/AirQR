"use strict";

// AirQR acoustic receiver — the JavaScript counterpart of internal/audio.
//
// It demodulates 16-tone continuous-phase MFSK out of a Float32Array of PCM,
// verifies each frame's CRC, and hands the surviving frames to the AIRQR2
// fountain decoder in fountain.js. Every constant here has to match
// internal/audio/modem.go exactly, because a receiver that disagrees with the
// sender about a tone frequency or a frame layout recovers nothing at all.
//
// Frames that fail CRC are dropped rather than repaired. That is what turns
// the channel's bit errors into the clean erasures the fountain code expects:
// the sender emits more symbols than K, so lost frames cost time, not data.
(function (global) {
  const TONES = 16;

  // Must mirror audio.Bands in internal/audio/modem.go.
  const Bands = {
    fast: { name: "fast", base: 1500, spacing: 200, symbolSec: 0.006, syncSec: 0.06, gapSec: 0.04 },
    audible: { name: "audible", base: 1200, spacing: 100, symbolSec: 0.012, syncSec: 0.06, gapSec: 0.04 },
    ultrasonic: { name: "ultrasonic", base: 19000, spacing: 50, symbolSec: 0.024, syncSec: 0.08, gapSec: 0.04 },
  };
  const BandNames = ["fast", "audible", "ultrasonic"];

  const toneFreq = (band, i) => band.base + i * band.spacing;
  const syncFreq = (band) => band.base + TONES * band.spacing;

  // bandFits reports whether every tone stays below Nyquist. A file captured
  // through a voice-optimised path is often resampled to 16 kHz or lower,
  // which silently removes the whole ultrasonic band.
  function bandFits(band, sampleRate) {
    return syncFreq(band) < sampleRate / 2;
  }

  function crc16(bytes, length) {
    let crc = 0xffff;
    for (let i = 0; i < length; i++) {
      crc ^= bytes[i] << 8;
      for (let b = 0; b < 8; b++) {
        crc = crc & 0x8000 ? ((crc << 1) ^ 0x1021) & 0xffff : (crc << 1) & 0xffff;
      }
    }
    return crc & 0xffff;
  }

  // goertzel returns the magnitude of one frequency over a window, normalised
  // so a full-amplitude tone reads about 1.0.
  function goertzel(samples, start, n, freq, sr) {
    if (start < 0 || start + n > samples.length || n <= 0) {
      return 0;
    }
    const c = 2 * Math.cos((2 * Math.PI * freq) / sr);
    let s1 = 0;
    let s2 = 0;
    for (let i = 0; i < n; i++) {
      const s0 = samples[start + i] + c * s1 - s2;
      s2 = s1;
      s1 = s0;
    }
    const power = s1 * s1 + s2 * s2 - c * s1 * s2;
    return (2 * Math.sqrt(power > 0 ? power : 0)) / n;
  }

  // symbolAt decides one symbol and reports how clearly it won. The margin
  // between the best and second-best tone is what distinguishes a correct
  // timing alignment from one straddling a symbol boundary.
  function symbolAt(samples, start, n, band, sr) {
    let best = 0;
    let second = 0;
    let bestIdx = 0;
    for (let i = 0; i < TONES; i++) {
      const m = goertzel(samples, start, n, toneFreq(band, i), sr);
      if (m > best) {
        second = best;
        best = m;
        bestIdx = i;
      } else if (m > second) {
        second = m;
      }
    }
    return { tone: bestIdx, margin: second > 0 ? best / second : Infinity };
  }

  function readFrame(samples, dataStart, symN, band, sr) {
    const search = Math.floor(symN / 2);
    const step = Math.max(1, Math.floor(symN / 16));
    let bestOff = dataStart;
    let bestScore = -1;
    for (let off = dataStart - search; off <= dataStart + search; off += step) {
      if (off < 0) continue;
      let score = 0;
      let probes = 0;
      for (let i = 0; i < 6; i++) {
        if (off + (i + 1) * symN > samples.length) break;
        let m = symbolAt(samples, off + i * symN, symN, band, sr).margin;
        if (!isFinite(m)) m = 100;
        score += m;
        probes++;
      }
      if (probes > 0 && score / probes > bestScore) {
        bestScore = score / probes;
        bestOff = off;
      }
    }
    if (bestScore < 0) return null;

    const read = (count) => {
      const out = new Uint8Array(count);
      for (let i = 0; i < count; i++) {
        if (bestOff + (2 * i + 2) * symN > samples.length) return null;
        const hi = symbolAt(samples, bestOff + 2 * i * symN, symN, band, sr).tone;
        const lo = symbolAt(samples, bestOff + (2 * i + 1) * symN, symN, band, sr).tone;
        out[i] = (hi << 4) | lo;
      }
      return out;
    };

    // Two bytes carry the type and length, so the frame can be sized before
    // anything about the transfer is known.
    const head = read(2);
    if (!head) return null;
    const total = head[1] + 2;
    if (total < 4 || total > 255) return null;
    return read(total);
  }

  // MIN_SYNC_RATIO is how far the strongest sync tone must rise above the
  // band's noise floor for the band to be worth decoding. Measured separation
  // is roughly 65x for a band carrying a transfer against 3x for an empty one.
  const MIN_SYNC_RATIO = 8;

  function medianOf(values) {
    if (values.length === 0) return 0;
    const sorted = Float64Array.from(values).sort();
    return sorted[Math.floor(sorted.length / 2)];
  }

  // syncTrack returns the magnitude of one frequency over a sliding window, at
  // every hop across the whole signal.
  //
  // Running a Goertzel per position costs a full pass over the window each
  // time, which for a half-minute recording is tens of millions of
  // multiply-adds per band — several seconds on a laptop and far worse on a
  // phone. Mixing down once and taking prefix sums yields the identical
  // quantity for the cost of a single pass, after which each position is two
  // subtractions. Mirrors syncTrack in internal/audio/demod.go.
  function syncTrack(samples, winN, hop, positions, freq, sr) {
    const n = samples.length;
    const sumI = new Float64Array(n + 1);
    const sumQ = new Float64Array(n + 1);
    const w = (2 * Math.PI * freq) / sr;
    for (let i = 0; i < n; i++) {
      const phase = w * i;
      const v = samples[i];
      sumI[i + 1] = sumI[i] + v * Math.cos(phase);
      sumQ[i + 1] = sumQ[i] + v * Math.sin(phase);
    }
    const out = new Float64Array(positions + 1);
    const scale = 2 / winN;
    for (let i = 0; i <= positions; i++) {
      const s = i * hop;
      const re = sumI[s + winN] - sumI[s];
      const im = sumQ[s + winN] - sumQ[s];
      out[i] = scale * Math.hypot(re, im);
    }
    return out;
  }

  // demodulate locates every block from its own sync tone and returns the
  // frames whose CRC verifies.
  function demodulate(samples, sampleRate, band) {
    const sr = sampleRate;
    const syncN = Math.floor(band.syncSec * sr);
    const symN = Math.floor(band.symbolSec * sr);
    if (syncN <= 0 || symN <= 0 || samples.length < syncN) return [];

    const hop = Math.max(1, Math.floor(0.002 * sr));
    const positions = Math.floor((samples.length - syncN) / hop);
    const mags = syncTrack(samples, syncN, hop, positions, syncFreq(band), sr);
    let peak = 0;
    for (let i = 0; i <= positions; i++) {
      if (mags[i] > peak) peak = mags[i];
    }
    if (peak <= 0) return [];

    // Reject the band outright unless some sync tone stands well clear of the
    // band's own noise floor, which the median of the track measures. Without
    // this, a band carrying nothing still produces candidates — the threshold
    // below is a fraction of that band's own peak, and 30% of noise is noise —
    // and each costs a full frame read before the CRC rejects it.
    const median = medianOf(mags);
    if (median > 0 && peak < MIN_SYNC_RATIO * median) return [];

    const threshold = 0.3 * peak;
    const guard = Math.max(1, Math.floor(syncN / hop));
    const candidates = [];
    for (let i = 0; i <= positions; i++) {
      if (mags[i] < threshold) continue;
      let isPeak = true;
      for (let j = i - guard; j <= i + guard; j++) {
        if (j >= 0 && j <= positions && mags[j] > mags[i]) {
          isPeak = false;
          break;
        }
      }
      if (isPeak && (candidates.length === 0 || i - candidates[candidates.length - 1] > guard)) {
        candidates.push(i);
      }
    }

    const frames = [];
    for (const c of candidates) {
      const f = readFrame(samples, c * hop + syncN, symN, band, sr);
      if (f) frames.push({ offset: c * hop, bytes: f });
    }
    return frames;
  }

  // parseFrame validates the CRC and returns a beacon or a data frame.
  function parseFrame(frame) {
    if (frame.length < 4) return null;
    if (frame[1] + 2 !== frame.length) return null;
    const want = (frame[frame.length - 2] << 8) | frame[frame.length - 1];
    if (crc16(frame, frame.length - 2) !== want) return null;
    const body = frame.subarray(2, frame.length - 2);
    const view = new DataView(body.buffer, body.byteOffset, body.byteLength);
    if (frame[0] === 0x01) {
      if (body.length !== 47) return null;
      return {
        kind: "beacon",
        session: view.getUint16(0),
        k: view.getUint16(2),
        t: view.getUint16(4),
        gzip: body[6] === 1,
        transferSize: view.getUint32(7),
        originalSize: view.getUint32(11),
        sha256: body.slice(15, 47),
      };
    }
    if (frame[0] === 0x02) {
      if (body.length < 6) return null;
      return {
        kind: "data",
        session: view.getUint16(0),
        esi: (body[2] << 16) | (body[3] << 8) | body[4],
        symbol: body.slice(5),
      };
    }
    return null;
  }

  // decode tries each band that fits under the sample rate and keeps whichever
  // recovers the most frames, so the receiver does not have to be told which
  // band a recording used.
  function decode(samples, sampleRate, only) {
    const names = only ? [only] : BandNames;
    let best = null;
    const tried = [];
    for (const name of names) {
      const band = Bands[name];
      if (!band || !bandFits(band, sampleRate)) {
        tried.push({ band: name, fits: false, beacons: 0, data: 0 });
        continue;
      }
      const beacons = [];
      const data = [];
      for (const f of demodulate(samples, sampleRate, band)) {
        const p = parseFrame(f.bytes);
        if (!p) continue;
        if (p.kind === "beacon") beacons.push(p);
        else data.push(p);
      }
      tried.push({ band: name, fits: true, beacons: beacons.length, data: data.length });
      if (!best || beacons.length + data.length > best.beacons.length + best.data.length) {
        best = { band: name, beacons, data };
      }
    }
    return { best, tried };
  }

  // maxFrameSamples is the longest a single block can run: its sync tone plus
  // the largest frame the framing allows. A live receiver has to retain at
  // least this much history, or a frame that straddles two scans is never seen
  // whole by either of them.
  function maxFrameSamples(band, sampleRate, maxFrameBytes) {
    return Math.ceil(band.syncSec * sampleRate) + maxFrameBytes * 2 * Math.ceil(band.symbolSec * sampleRate);
  }

  // LiveReceiver decodes continuously from a microphone instead of from a
  // finished file, the way the camera scanner accumulates QR frames.
  //
  // It keeps a rolling window just longer than one block and re-scans it as
  // audio arrives, so a block is always seen whole by at least one scan. Frames
  // already accepted are ignored, which makes the repeated scans harmless: the
  // same block found twice yields the same symbol, and the fountain decoder
  // treats a repeat as redundant anyway.
  //
  // The window is bounded, so the cost per scan does not grow with how long
  // someone listens — a receiver that re-scanned the whole session would get
  // slower the longer it ran, which is exactly when it must not.
  class LiveReceiver {
    constructor(sampleRate, options) {
      const opts = options || {};
      this.sampleRate = sampleRate;
      this.maxFrameBytes = opts.maxFrameBytes || 80;
      this.scanSamples = Math.max(1, Math.round((opts.scanSeconds || 0.4) * sampleRate));
      this.bandNames = (opts.bands || BandNames).filter((n) => bandFits(Bands[n], sampleRate));
      this.band = null; // locks to the first band that yields a valid frame
      this.seen = new Set();
      this.beacon = null;
      this.frames = [];
      this.level = 0;
      this.sinceScan = 0;
      this.buf = new Float32Array(Math.max(sampleRate, 1));
      this.len = 0;
      this.retain = this.bandNames.reduce(
        (m, n) => Math.max(m, maxFrameSamples(Bands[n], sampleRate, this.maxFrameBytes)),
        sampleRate,
      );
    }

    // retainFor shrinks the window once a band is locked: the ultrasonic band
    // needs four seconds of history, the fast band needs one.
    retainFor() {
      return this.band
        ? maxFrameSamples(Bands[this.band], this.sampleRate, this.maxFrameBytes)
        : this.retain;
    }

    append(chunk) {
      if (this.len + chunk.length > this.buf.length) {
        const grown = new Float32Array(Math.max(this.buf.length * 2, this.len + chunk.length));
        grown.set(this.buf.subarray(0, this.len));
        this.buf = grown;
      }
      this.buf.set(chunk, this.len);
      this.len += chunk.length;
    }

    trim() {
      const keep = this.retainFor() + this.scanSamples;
      if (this.len > keep) {
        this.buf.copyWithin(0, this.len - keep, this.len);
        this.len = keep;
      }
    }

    // push accepts one chunk of microphone audio and returns the frames that
    // became available because of it.
    push(chunk) {
      let sum = 0;
      for (let i = 0; i < chunk.length; i++) sum += chunk[i] * chunk[i];
      this.level = chunk.length ? Math.sqrt(sum / chunk.length) : 0;

      this.append(chunk);
      this.sinceScan += chunk.length;
      if (this.sinceScan < this.scanSamples) return [];
      this.sinceScan = 0;

      const window = this.buf.subarray(0, this.len);
      const names = this.band ? [this.band] : this.bandNames;
      const fresh = [];
      for (const name of names) {
        for (const found of demodulate(window, this.sampleRate, Bands[name])) {
          const parsed = parseFrame(found.bytes);
          if (!parsed) continue;
          const key =
            parsed.kind === "beacon"
              ? `b:${parsed.session}`
              : `d:${parsed.session}:${parsed.esi}`;
          if (this.seen.has(key)) continue;
          this.seen.add(key);
          this.band = name;
          if (parsed.kind === "beacon") {
            if (!this.beacon) this.beacon = parsed;
          } else {
            this.frames.push(parsed);
          }
          fresh.push(parsed);
        }
        if (this.band) break;
      }
      this.trim();
      return fresh;
    }

    reset() {
      this.band = null;
      this.seen.clear();
      this.beacon = null;
      this.frames = [];
      this.len = 0;
      this.sinceScan = 0;
      this.level = 0;
    }
  }

  const api = { TONES, Bands, BandNames, toneFreq, syncFreq, bandFits, crc16, goertzel, demodulate, parseFrame, decode, LiveReceiver, maxFrameSamples };
  global.AirQRAudio = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(typeof self !== "undefined" ? self : globalThis);
