"use strict";

// AIRQR2 fountain decoder — the JavaScript counterpart of internal/airqr/fountain.go.
//
// It reconstructs a transfer from any K linearly independent coded symbols using
// incremental Gauss-Jordan elimination over GF(256). The GF(256) field, the
// splitmix32 PRNG, and the coefficient derivation are bit-for-bit identical to
// the Go encoder, so coefficients are never transmitted — the decoder regenerates
// them from each frame's ESI. (Cross-language interop is pinned by
// fountain.test.mjs decoding a fixture produced by the Go encoder.)
(function (global) {
  // GF(256) exp/log tables: primitive polynomial 0x11D, generator 2 (the QR
  // standard field). gfExp is doubled so index sums below 510 never wrap.
  const gfExp = new Uint8Array(512);
  const gfLog = new Uint8Array(256);
  (function buildTables() {
    let x = 1;
    for (let i = 0; i < 255; i++) {
      gfExp[i] = x;
      gfLog[x] = i;
      x <<= 1;
      if (x & 0x100) {
        x ^= 0x11d;
      }
    }
    for (let i = 255; i < 512; i++) {
      gfExp[i] = gfExp[i - 255];
    }
  })();

  function gfMul(a, b) {
    if (a === 0 || b === 0) {
      return 0;
    }
    return gfExp[gfLog[a] + gfLog[b]];
  }

  function gfInv(a) {
    return gfExp[255 - gfLog[a]];
  }

  // splitmix32 — mirrors fountainRNG in fountain.go. Math.imul performs the
  // 32-bit multiply; `>>> 0` keeps every intermediate unsigned, matching Go's
  // uint32 wraparound exactly.
  function makeRng(seed) {
    let state = seed >>> 0;
    return function next() {
      state = (state + 0x9e3779b9) >>> 0;
      let z = state;
      z = Math.imul(z ^ (z >>> 16), 0x21f0aaad) >>> 0;
      z = Math.imul(z ^ (z >>> 15), 0x735a2d97) >>> 0;
      return (z ^ (z >>> 15)) >>> 0;
    };
  }

  // coeffs returns the GF(256) coefficient vector (length k) for an ESI.
  function coeffs(esi, k) {
    const out = new Uint8Array(k);
    if (esi < k) {
      out[esi] = 1;
      return out;
    }
    const next = makeRng(esi);
    let nonzero = false;
    for (let i = 0; i < k; i++) {
      out[i] = next() & 0xff;
      if (out[i] !== 0) {
        nonzero = true;
      }
    }
    if (!nonzero) {
      out[esi % k] = 1;
    }
    return out;
  }

  class Decoder {
    constructor(k, t) {
      this.k = k;
      this.t = t;
      this.coeffRows = new Array(k).fill(null);
      this.dataRows = new Array(k).fill(null);
      this.rank = 0;
    }

    // add folds one coded symbol into the system and returns true once the
    // decoder reaches full rank. data is a Uint8Array of length t.
    add(esi, data) {
      if (!data || data.length !== this.t) {
        return this.rank === this.k;
      }
      const k = this.k;
      const t = this.t;
      const row = coeffs(esi, k);
      const acc = new Uint8Array(data); // copy

      // Reduce against existing pivots (kept in RREF), zeroing their columns.
      for (let col = 0; col < k; col++) {
        if (row[col] === 0 || this.coeffRows[col] === null) {
          continue;
        }
        const factor = row[col];
        const pc = this.coeffRows[col];
        const pd = this.dataRows[col];
        for (let i = 0; i < k; i++) {
          row[i] ^= gfMul(factor, pc[i]);
        }
        for (let i = 0; i < t; i++) {
          acc[i] ^= gfMul(factor, pd[i]);
        }
      }

      let pivot = -1;
      for (let col = 0; col < k; col++) {
        if (row[col] !== 0) {
          pivot = col;
          break;
        }
      }
      if (pivot < 0) {
        return this.rank === this.k; // redundant symbol
      }

      const inv = gfInv(row[pivot]);
      for (let i = 0; i < k; i++) {
        row[i] = gfMul(row[i], inv);
      }
      for (let i = 0; i < t; i++) {
        acc[i] = gfMul(acc[i], inv);
      }

      // Eliminate this pivot column from existing pivot rows to keep RREF.
      for (let col = 0; col < k; col++) {
        if (this.coeffRows[col] === null || this.coeffRows[col][pivot] === 0) {
          continue;
        }
        const factor = this.coeffRows[col][pivot];
        const pc = this.coeffRows[col];
        const pd = this.dataRows[col];
        for (let i = 0; i < k; i++) {
          pc[i] ^= gfMul(factor, row[i]);
        }
        for (let i = 0; i < t; i++) {
          pd[i] ^= gfMul(factor, acc[i]);
        }
      }

      this.coeffRows[pivot] = row;
      this.dataRows[pivot] = acc;
      this.rank++;
      return this.rank === this.k;
    }

    get complete() {
      return this.rank === this.k;
    }

    // packed returns the K*T concatenated source bytes once complete. Callers
    // truncate to the transfer size, gunzip if needed, and verify the hash.
    packed() {
      if (this.rank !== this.k) {
        return null;
      }
      const out = new Uint8Array(this.k * this.t);
      for (let col = 0; col < this.k; col++) {
        out.set(this.dataRows[col], col * this.t);
      }
      return out;
    }
  }

  const api = { Decoder, coeffs, gfMul, gfInv };
  global.AirQRFountain = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(typeof self !== "undefined" ? self : globalThis);
