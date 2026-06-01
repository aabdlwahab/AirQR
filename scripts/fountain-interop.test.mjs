// Cross-language interop test: decode a fixture produced by the Go encoder
// (internal/airqr TestFountainWriteInteropFixture) using the browser decoder,
// proving the GF(256) field, splitmix32 PRNG, and Gauss-Jordan solver match.
//
// Run from the repo root: node scripts/fountain-interop.test.mjs
import { readFileSync } from "node:fs";
import { gunzipSync } from "node:zlib";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const require = createRequire(import.meta.url);
const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const { Decoder } = require(join(root, "cmd/airqr/web/fountain.js"));

const fixture = JSON.parse(
  readFileSync(join(root, "internal/airqr/testdata/fountain.interop.json"), "utf8"),
);
const expected = Buffer.from(fixture.originalBase64, "base64");

function base64UrlToBytes(text) {
  const padded = text + "=".repeat((4 - (text.length % 4)) % 4);
  return Buffer.from(padded.replace(/-/g, "+").replace(/_/g, "/"), "base64");
}

let decoder = null;
let meta = null;
let completedAt = -1;
for (let i = 0; i < fixture.frames.length; i++) {
  const parts = fixture.frames[i].split("|");
  if (parts.length !== 10 || parts[0] !== "AIRQR2") {
    throw new Error(`frame ${i} is not an AIRQR2 payload`);
  }
  const esi = Number(parts[2]);
  const k = Number(parts[3]);
  const t = Number(parts[4]);
  const data = new Uint8Array(base64UrlToBytes(parts[9]));
  if (decoder === null) {
    decoder = new Decoder(k, t);
    meta = { flags: parts[5], transferSize: Number(parts[6]), originalSize: Number(parts[7]) };
  }
  if (decoder.add(esi, data) && completedAt < 0) {
    completedAt = i;
    break;
  }
}

if (!decoder || !decoder.complete) {
  throw new Error("decoder did not reach full rank from the fixture frames");
}

let bytes = Buffer.from(decoder.packed().subarray(0, meta.transferSize));
if (meta.flags === "z") {
  bytes = gunzipSync(bytes);
}

if (bytes.length !== meta.originalSize) {
  throw new Error(`size mismatch: got ${bytes.length}, expected ${meta.originalSize}`);
}
if (!bytes.equals(expected)) {
  throw new Error("decoded payload does not match the Go-encoded original");
}

console.log(
  `OK: JS decoder reconstructed ${bytes.length} bytes from Go-encoded fountain frames ` +
    `(K=${fixture.k}, T=${fixture.t}, completed after ${completedAt + 1} frames).`,
);
