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
  // Parallel bands: 2 bits on each of many subcarriers at once. The 12 ms
  // cyclic prefix has to outlast the room's longest significant reflection —
  // anything later smears one symbol into the next.
  Bands["audible-fast"] = { name: "audible-fast", base: 1200, spacing: 50, symbolSec: 0.02,
    syncSec: 0.06, gapSec: 0.04, carriers: 32, prefixSec: 0.012, suffixSec: 0.002, parity: 16 };
  Bands["ultrasonic-fast"] = { name: "ultrasonic-fast", base: 19000, spacing: 50, symbolSec: 0.02,
    syncSec: 0.08, gapSec: 0.04, carriers: 16, prefixSec: 0.012, suffixSec: 0.002, parity: 16 };
  Bands["ultrasonic-wide"] = { name: "ultrasonic-wide", base: 19000, spacing: 50, symbolSec: 0.02,
    syncSec: 0.08, gapSec: 0.04, carriers: 32, prefixSec: 0.012, suffixSec: 0.002, parity: 16 };
  // ultrawide runs 19.0-21.15 kHz with its sync tone at 21.2 kHz and carries
  // three bits per subcarrier instead of two. It stops short of the 21.8 kHz
  // ceiling the 48 kHz reconstruction filter imposes, because margin per
  // subcarrier collapses before the spectrum runs out.
  Bands["ultrawide"] = { name: "ultrawide", base: 19000, spacing: 50, symbolSec: 0.02,
    syncSec: 0.08, gapSec: 0.04, carriers: 44, prefixSec: 0.008, suffixSec: 0.002,
    parity: 24, phase: 3 };

  // ultrawide-max trades reverberation guard for speed: a 4 ms cyclic prefix
  // instead of 8 ms, over 48 subcarriers. Only worth choosing with the devices
  // practically touching.
  Bands["ultrawide-max"] = { name: "ultrawide-max", base: 19000, spacing: 50, symbolSec: 0.02,
    syncSec: 0.08, gapSec: 0.04, carriers: 48, prefixSec: 0.004, suffixSec: 0.002,
    parity: 24, phase: 3 };

  const BandNames = ["fast", "audible", "ultrasonic", "audible-fast", "ultrasonic-fast", "ultrasonic-wide", "ultrawide", "ultrawide-max"];

  const toneFreq = (band, i) => band.base + i * band.spacing;
  const isParallel = (band) => (band.carriers || 0) > 0;
  const carrierFreq = (band, i) => band.base + i * band.spacing;
  const blockSec = (band) => band.prefixSec + band.symbolSec + band.suffixSec;
  // Bits per subcarrier per symbol: 2 for QPSK, 3 for 8-PSK.
  const phaseBits = (band) => band.phase || 2;
  const phaseCount = (band) => 1 << phaseBits(band);
  const bitsPerBlock = (band) => phaseBits(band) * band.carriers;
  const blocksForBytes = (band, n) => Math.ceil((n * 8) / bitsPerBlock(band));
  const syncFreq = (band) =>
    band.base + (isParallel(band) ? band.carriers : TONES) * band.spacing;

  // bandFits reports whether every tone stays below Nyquist. A file captured
  // through a voice-optimised path is often resampled to 16 kHz or lower,
  // which silently removes the whole ultrasonic band.
  function bandFits(band, sampleRate) {
    return syncFreq(band) < sampleRate / 2;
  }

  // Reed-Solomon over GF(256), mirroring internal/audio/rs.go.
  //
  // Measured over the air, about half of all frames failed their CRC even
  // though the differential phases were landing well inside their quadrants:
  // a few subcarriers sit in nulls that reflections comb into the response and
  // corrupt a byte or two out of seventy. Without correction those frames are
  // worth nothing. Errors arrive in whole bytes here, which is the shape
  // Reed-Solomon handles best.
  const rsExp = new Uint8Array(512);
  const rsLog = new Uint8Array(256);
  (function buildRS() {
    let x = 1;
    for (let i = 0; i < 255; i++) {
      rsExp[i] = x;
      rsLog[x] = i;
      x <<= 1;
      if (x & 0x100) x ^= 0x11d;
    }
    for (let i = 255; i < 512; i++) rsExp[i] = rsExp[i - 255];
  })();

  const rsMul = (a, b) => (a === 0 || b === 0 ? 0 : rsExp[rsLog[a] + rsLog[b]]);
  const rsInv = (a) => rsExp[255 - rsLog[a]];
  const rsDiv = (a, b) => (a === 0 ? 0 : rsExp[(rsLog[a] - rsLog[b] + 255) % 255]);
  const rsPow = (a, n) => {
    if (a === 0) return 0;
    let e = (rsLog[a] * n) % 255;
    if (e < 0) e += 255;
    return rsExp[e];
  };

  // Ascending order throughout: p[i] is the coefficient of x^i.
  function rsPolyMulAsc(p, q) {
    const out = new Uint8Array(p.length + q.length - 1);
    for (let i = 0; i < p.length; i++) {
      if (p[i] === 0) continue;
      for (let j = 0; j < q.length; j++) out[i + j] ^= rsMul(p[i], q[j]);
    }
    return out;
  }

  function rsPolyEvalAsc(p, x) {
    let y = 0;
    for (let i = p.length - 1; i >= 0; i--) y = rsMul(y, x) ^ p[i];
    return y;
  }

  // rsDecode corrects up to nsym/2 byte errors in data followed by nsym parity
  // bytes, or returns null.
  function rsDecode(code, nsym) {
    if (nsym <= 0 || code.length <= nsym || code.length > 255) return null;
    const work = Uint8Array.from(code);
    const n = work.length;

    const synd = new Uint8Array(nsym);
    let clean = true;
    for (let j = 0; j < nsym; j++) {
      let v = 0;
      for (let i = 0; i < n; i++) v = rsMul(v, rsExp[j]) ^ work[i];
      synd[j] = v;
      if (v !== 0) clean = false;
    }
    if (clean) return work.subarray(0, n - nsym);

    // Berlekamp-Massey.
    let lam = [1];
    let b = [1];
    let L = 0;
    let m = 1;
    let bb = 1;
    for (let i = 0; i < nsym; i++) {
      let d = synd[i];
      for (let k = 1; k <= L && k < lam.length; k++) d ^= rsMul(lam[k], synd[i - k]);
      if (d === 0) {
        m++;
      } else if (2 * L <= i) {
        const t = lam.slice();
        const scale = rsDiv(d, bb);
        while (lam.length < b.length + m) lam.push(0);
        for (let k = 0; k < b.length; k++) lam[k + m] ^= rsMul(scale, b[k]);
        L = i + 1 - L;
        b = t;
        bb = d;
        m = 1;
      } else {
        const scale = rsDiv(d, bb);
        while (lam.length < b.length + m) lam.push(0);
        for (let k = 0; k < b.length; k++) lam[k + m] ^= rsMul(scale, b[k]);
        m++;
      }
    }
    if (L <= 0 || 2 * L > nsym) return null;
    lam = lam.slice(0, L + 1);

    // Chien search.
    const positions = [];
    const locators = [];
    for (let e = 0; e < n; e++) {
      if (rsPolyEvalAsc(lam, rsInv(rsPow(2, e))) === 0) {
        positions.push(n - 1 - e);
        locators.push(rsPow(2, e));
      }
    }
    if (positions.length !== L) return null;

    // Forney, using the product form so no formal derivative is needed.
    let omega = rsPolyMulAsc(synd, Uint8Array.from(lam));
    if (omega.length > nsym) omega = omega.subarray(0, nsym);
    for (let i = 0; i < positions.length; i++) {
      const xi = locators[i];
      const xiInv = rsInv(xi);
      let den = 1;
      for (let j = 0; j < locators.length; j++) {
        if (j !== i) den = rsMul(den, 1 ^ rsMul(locators[j], xiInv));
      }
      if (den === 0) return null;
      work[positions[i]] ^= rsDiv(rsPolyEvalAsc(omega, xiInv), den);
    }

    // A genuine correction zeroes every syndrome; without this check a
    // miscorrection past capacity would hand back confidently wrong bytes.
    for (let j = 0; j < nsym; j++) {
      let v = 0;
      for (let i = 0; i < n; i++) v = rsMul(v, rsExp[j]) ^ work[i];
      if (v !== 0) return null;
    }
    return work.subarray(0, n - nsym);
  }

  // majority returns the value at least two of three agree on. The frame length
  // is sent three times because the receiver must know how many bytes to read
  // before it can run the correction that would have fixed a corrupt length.
  const majority = (a, b2, c) => (a === b2 || a === c ? a : b2 === c ? b2 : a);

  const maxFrameLen = (parity) => (parity > 0 ? 255 - parity : 255);

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

  // GRAY[bits][q] is the value carried by constellation index q, so a phase
  // error landing on a neighbouring point costs one bit rather than several.
  // At two bits the mapping is its own inverse; at three it is not, so the
  // receiver uses this direction only.
  // The framed size of a beacon: type, length, the 47-byte body and the CRC.
  // It is the shortest frame sent, so it bounds a safe probe length.
  const BEACON_FRAME_LEN = 2 + 47 + 2;

  const GRAY = {};
  (function buildGray() {
    for (const bits of [2, 3, 4]) {
      const n = 1 << bits;
      const d = new Int8Array(n);
      for (let q = 0; q < n; q++) d[q] = q ^ (q >> 1);
      GRAY[bits] = d;
    }
  })();

  // Correlator tables are the same for every symbol window, so build them once
  // per band and sample rate rather than calling trig inside the inner loop.
  const tableCache = new Map();
  function carrierTables(band, sr) {
    const key = `${band.name}@${sr}`;
    let t = tableCache.get(key);
    if (t) return t;
    const symbolN = Math.floor(band.symbolSec * sr);
    const cos = [];
    const sin = [];
    for (let k = 0; k < band.carriers; k++) {
      const w = (2 * Math.PI * carrierFreq(band, k)) / sr;
      const c = new Float64Array(symbolN);
      const s2 = new Float64Array(symbolN);
      for (let n = 0; n < symbolN; n++) {
        c[n] = Math.cos(w * n);
        s2[n] = Math.sin(w * n);
      }
      cos.push(c);
      sin.push(s2);
    }
    t = { cos, sin, symbolN };
    tableCache.set(key, t);
    return t;
  }

  // carrierPhasors returns the complex amplitude of every subcarrier over one
  // useful symbol window, skipping the cyclic prefix.
  function carrierPhasors(samples, blockStart, prefixN, band, sr, out) {
    const { cos, sin, symbolN } = carrierTables(band, sr);
    const start = blockStart + prefixN;
    const M = band.carriers;
    if (!out) out = { re: new Float64Array(M), im: new Float64Array(M) };
    if (start < 0 || start + symbolN > samples.length) {
      out.re.fill(0);
      out.im.fill(0);
      return out;
    }
    for (let k = 0; k < M; k++) {
      const c = cos[k];
      const s2 = sin[k];
      let re = 0;
      let im = 0;
      for (let n = 0; n < symbolN; n++) {
        const v = samples[start + n];
        re += v * c[n];
        im += v * s2[n];
      }
      out.re[k] = re / symbolN;
      out.im[k] = im / symbolN;
    }
    return out;
  }

  function readFrameParallel(samples, dataStart, band, sr) {
    const prefixN = Math.floor(band.prefixSec * sr);
    const blockN = Math.floor(blockSec(band) * sr);
    const M = band.carriers;

    // decodeAt reads count data symbols after the reference symbol, and reports
    // how cleanly the differential phases landed in their quadrants.
    const pb = phaseBits(band);
    const nPhases = phaseCount(band);
    const phaseStep = (2 * Math.PI) / nPhases;
    const dataOf = GRAY[pb];

    const decodeAt = (off, count) => {
      if (off < 0 || off + (count + 1) * blockN > samples.length) return null;
      let prev = carrierPhasors(samples, off, prefixN, band, sr);
      const bits = new Int8Array(count * bitsPerBlock(band));
      let at = 0;
      let margin = 0;
      for (let blk = 1; blk <= count; blk++) {
        const cur = carrierPhasors(samples, off + blk * blockN, prefixN, band, sr);
        for (let k = 0; k < M; k++) {
          // conj(cur) * prev recovers +delta-phi: correlating a sin() carrier
          // against a cos/sin pair yields a phasor at (pi/2 - phi), so the
          // other order would decode every quadrant backwards.
          const re = cur.re[k] * prev.re[k] + cur.im[k] * prev.im[k];
          const im = -cur.im[k] * prev.re[k] + cur.re[k] * prev.im[k];
          const angle = Math.atan2(im, re);
          const q = Math.round(angle / phaseStep) & (nPhases - 1);
          let err = Math.abs(angle - q * phaseStep);
          while (err > Math.PI) err = 2 * Math.PI - err;
          // Normalised against the decision boundary, half a constellation
          // step: 1.0 is dead centre, 0.0 is on the edge.
          margin += 1 - err / (phaseStep / 2);
          const v = dataOf[q];
          for (let i = pb - 1; i >= 0; i--) bits[at++] = (v >> i) & 1;
        }
        prev = { re: cur.re.slice(), im: cur.im.slice() };
      }
      return { bits, margin: margin / (count * M) };
    };

    // Align on the phase-decision margin, not on received energy: energy hardly
    // varies with alignment, so an energy metric settles half a symbol off
    // where every bit is noise. The search spans the sync detector's own
    // resolution plus the prefix, and no further — a wider sweep would invite
    // locking onto the neighbouring symbol.
    const search = prefixN + Math.floor(0.002 * sr);
    const step = Math.max(1, Math.floor(prefixN / 4));
    // Probe no further than the shortest frame reaches. A wide band packs a
    // frame into very few symbols, and a fixed three-symbol probe would read
    // past it into the gap and the next sync tone, aligning on noise.
    const probeBlocks = Math.min(3, Math.max(1, blocksForBytes(band, 3 + BEACON_FRAME_LEN + (band.parity || 0))));
    let bestOff = -1;
    let bestMargin = -2;
    for (let off = dataStart - search; off <= dataStart + search; off += step) {
      const probe = decodeAt(off, probeBlocks);
      if (probe && probe.margin > bestMargin) {
        bestMargin = probe.margin;
        bestOff = off;
      }
    }
    if (bestOff < 0) return null;

    const bitsToBytes = (bits, n) => {
      const out = new Uint8Array(n);
      for (let i = 0; i < n; i++) {
        let by = 0;
        for (let j = 0; j < 8; j++) by = (by << 1) | (bits[i * 8 + j] & 1);
        out[i] = by;
      }
      return out;
    };

    const parity = band.parity || 0;
    if (parity > 0) {
      const h = decodeAt(bestOff, blocksForBytes(band, 3));
      if (!h || h.bits.length < 24) return null;
      const hb = bitsToBytes(h.bits, 3);
      const frameLen = majority(hb[0], hb[1], hb[2]);
      if (frameLen < 4 || frameLen > maxFrameLen(parity)) return null;
      const airLen = 3 + frameLen + parity;
      const all = decodeAt(bestOff, blocksForBytes(band, airLen));
      if (!all || all.bits.length < airLen * 8) return null;
      const air = bitsToBytes(all.bits, airLen);
      const fixed = rsDecode(air.subarray(3, airLen), parity);
      return fixed ? Uint8Array.from(fixed) : null;
    }

    const head = decodeAt(bestOff, blocksForBytes(band, 2));
    if (!head || head.bits.length < 16) return null;
    const total = bitsToBytes(head.bits, 2)[1] + 2;
    if (total < 4 || total > 255) return null;
    const all = decodeAt(bestOff, blocksForBytes(band, total));
    if (!all || all.bits.length < total * 8) return null;
    return bitsToBytes(all.bits, total);
  }

  // syncCandidates finds every position where a sync tone stands clear of the
  // band's own noise floor. Shared by both modulations.
  function syncCandidates(samples, sampleRate, band) {
    const sr = sampleRate;
    const syncN = Math.floor(band.syncSec * sr);
    if (syncN <= 0 || samples.length < syncN) return { syncN, hop: 1, list: [] };
    const hop = Math.max(1, Math.floor(0.002 * sr));
    const positions = Math.floor((samples.length - syncN) / hop);
    if (positions < 0) return { syncN, hop, list: [] };
    const mags = syncTrack(samples, syncN, hop, positions, syncFreq(band), sr);
    let peak = 0;
    for (let i = 0; i <= positions; i++) if (mags[i] > peak) peak = mags[i];
    if (peak <= 0) return { syncN, hop, list: [] };
    const median = medianOf(mags);
    if (median > 0 && peak < MIN_SYNC_RATIO * median) return { syncN, hop, list: [] };

    const threshold = 0.3 * peak;
    const guard = Math.max(1, Math.floor(syncN / hop));
    const list = [];
    for (let i = 0; i <= positions; i++) {
      if (mags[i] < threshold) continue;
      let isPeak = true;
      for (let j = i - guard; j <= i + guard; j++) {
        if (j >= 0 && j <= positions && mags[j] > mags[i]) {
          isPeak = false;
          break;
        }
      }
      if (isPeak && (list.length === 0 || i - list[list.length - 1] > guard)) list.push(i);
    }
    return { syncN, hop, list };
  }

  function demodulateParallel(samples, sampleRate, band) {
    const { syncN, hop, list } = syncCandidates(samples, sampleRate, band);
    const frames = [];
    for (const c of list) {
      const f = readFrameParallel(samples, c * hop + syncN, band, sampleRate);
      if (f) frames.push({ offset: c * hop, bytes: f });
    }
    return frames;
  }

  // demodulate locates every block from its own sync tone and returns the
  // frames whose CRC verifies.
  function demodulate(samples, sampleRate, band) {
    if (isParallel(band)) return demodulateParallel(samples, sampleRate, band);
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
    const sync = Math.ceil(band.syncSec * sampleRate);
    if (isParallel(band)) {
      // One reference symbol plus the data symbols the frame occupies.
      return sync + (1 + blocksForBytes(band, maxFrameBytes)) * Math.ceil(blockSec(band) * sampleRate);
    }
    return sync + maxFrameBytes * 2 * Math.ceil(band.symbolSec * sampleRate);
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

  const api = { TONES, Bands, BandNames, toneFreq, syncFreq, bandFits, crc16, goertzel, demodulate, parseFrame, decode, LiveReceiver, maxFrameSamples, rsDecode, isParallel, carrierFreq, blockSec, bitsPerBlock };
  global.AirQRAudio = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(typeof self !== "undefined" ? self : globalThis);
