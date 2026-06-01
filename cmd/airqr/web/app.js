"use strict";

const els = {
  app: document.querySelector("#app"),
  video: document.querySelector("#video"),
  canvas: document.querySelector("#canvas"),
  supportStatus: document.querySelector("#supportStatus"),
  startButton: document.querySelector("#startButton"),
  stopButton: document.querySelector("#stopButton"),
  resetButton: document.querySelector("#resetButton"),
  scanAnotherButton: document.querySelector("#scanAnotherButton"),
  cameraSelect: document.querySelector("#cameraSelect"),
  frameCount: document.querySelector("#frameCount"),
  frameTotal: document.querySelector("#frameTotal"),
  remainingText: document.querySelector("#remainingText"),
  progressBar: document.querySelector("#progressBar"),
  frameDots: document.querySelector("#frameDots"),
  phaseLabel: document.querySelector("#phaseLabel"),
  sessionValue: document.querySelector("#sessionValue"),
  detailSession: document.querySelector("#detailSession"),
  modeValue: document.querySelector("#modeValue"),
  rateValue: document.querySelector("#rateValue"),
  detailRate: document.querySelector("#detailRate"),
  elapsedValue: document.querySelector("#elapsedValue"),
  etaValue: document.querySelector("#etaValue"),
  detailElapsed: document.querySelector("#detailElapsed"),
  detailEta: document.querySelector("#detailEta"),
  statusText: document.querySelector("#statusText"),
  detailStatus: document.querySelector("#detailStatus"),
  lastFrameValue: document.querySelector("#lastFrameValue"),
  missingValue: document.querySelector("#missingValue"),
  resultSubtitle: document.querySelector("#resultSubtitle"),
  resultSheet: document.querySelector("#resultSheet"),
  settingsSheet: document.querySelector("#settingsSheet"),
  detailsSheet: document.querySelector("#detailsSheet"),
  menuButton: document.querySelector("#menuButton"),
  detailsButton: document.querySelector("#detailsButton"),
  resultText: document.querySelector("#resultText"),
  copyButton: document.querySelector("#copyButton"),
  saveButton: document.querySelector("#saveButton"),
  accessUrl: document.querySelector("#accessUrl"),
  copyUrlButton: document.querySelector("#copyUrlButton"),
  networkHint: document.querySelector("#networkHint"),
  manualFrames: document.querySelector("#manualFrames"),
  addManualButton: document.querySelector("#addManualButton"),
};

const state = {
  stream: null,
  detector: null,
  running: false,
  scanning: false,
  session: "",
  metadata: null,
  frames: new Map(),
  assembling: false,
  completed: false,
  lastPayload: "",
  lastPayloadAt: 0,
  lastCanvasFallback: false,
  decoderName: "",
  lastFrameIndex: 0,
  lastFrameAt: 0,
  scanCount: 0,
  rateBaseline: 0,
  rateTimer: null,
  // AIRQR2 fountain decode state (null until an AIRQR2 frame arrives).
  fountain: null,
  fountainRank: 0,
  fountainEsi: null,
  // Timing: set when the first frame lands; drives elapsed + adaptive ETA.
  transferStartAt: 0,
  clockTimer: null,
};

const RATE_WINDOW_MS = 500;

init();

function init() {
  els.startButton.addEventListener("click", startScanning);
  els.stopButton.addEventListener("click", stopCamera);
  els.resetButton.addEventListener("click", () => { resetTransfer(); closeSheets(); });
  els.scanAnotherButton.addEventListener("click", startScanning);
  els.copyButton.addEventListener("click", copyResult);
  els.saveButton.addEventListener("click", saveResult);
  els.copyUrlButton.addEventListener("click", copyAccessUrl);
  els.addManualButton.addEventListener("click", addManualFrames);
  els.cameraSelect.addEventListener("change", restartCamera);
  els.menuButton.addEventListener("click", () => openSheet(els.settingsSheet));
  els.detailsButton.addEventListener("click", () => { fillDetails(); openSheet(els.detailsSheet); });
  for (const sheet of [els.settingsSheet, els.detailsSheet]) {
    sheet.addEventListener("click", (event) => {
      if (event.target === sheet || event.target.closest(".sheet-close")) {
        closeSheets();
      }
    });
  }
  populateAccessInfo();
  updateSupportStatus();
  updateUi();
  registerServiceWorker();
}

function openSheet(sheet) {
  closeSheets();
  sheet.hidden = false;
}

function closeSheets() {
  els.settingsSheet.hidden = true;
  els.detailsSheet.hidden = true;
}

function fillDetails() {
  els.detailRate.textContent = `${els.rateValue.textContent} fps`;
  els.detailStatus.textContent = els.statusText.textContent;
  updateTiming();
}

function registerServiceWorker() {
  // Caches the scanner for offline use so it runs after a single load, with no
  // server or network. Requires a secure context (HTTPS or localhost).
  if (!("serviceWorker" in navigator) || !isSecureContext) {
    return;
  }
  navigator.serviceWorker.register("sw.js").catch(() => {
    // Offline support is a progressive enhancement; scanning still works without it.
  });
}

async function updateSupportStatus() {
  if (!isSecureContext) {
    setSupport("warn", "Camera needs HTTPS or localhost");
    return;
  }
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    setSupport("warn", "Camera API unavailable");
    return;
  }
  if (!("BarcodeDetector" in window)) {
    if (window.jsQR) {
      state.decoderName = "jsQR";
      setSupport("ready", "Ready");
      return;
    }
    setSupport("warn", "QR decoder unavailable");
    setStatus("Manual frame input ready");
    return;
  }

  try {
    const formats = await window.BarcodeDetector.getSupportedFormats?.();
    if (formats && !formats.includes("qr_code")) {
      if (window.jsQR) {
        state.decoderName = "jsQR";
        setSupport("ready", "Ready");
        return;
      }
      setSupport("warn", "QR decoder unavailable");
      return;
    }
    state.detector = new window.BarcodeDetector({ formats: ["qr_code"] });
    state.decoderName = "BarcodeDetector";
    setSupport("ready", "Ready");
  } catch (error) {
    if (window.jsQR) {
      state.decoderName = "jsQR";
      setSupport("ready", "Ready");
      return;
    }
    setSupport("warn", "QR decoder unavailable");
  }
}

function setSupport(kind, text) {
  els.supportStatus.classList.remove("ready", "warn");
  if (kind) {
    els.supportStatus.classList.add(kind);
  }
  els.supportStatus.textContent = text;
  // The Signal UI has no dedicated support badge, so surface warnings (no
  // camera, no decoder, insecure context) on the HUD status line instead.
  if (kind === "warn") {
    setStatus(text);
  }
}

async function startScanning() {
  // "Start scanning" / "Scan again": clear a finished transfer first so the
  // camera reopens into a fresh capture instead of the completed state.
  if (state.completed) {
    resetTransfer();
  }
  await startCamera();
}

async function startCamera() {
  if (!isSecureContext) {
    setStatus("Open over HTTPS or localhost");
    return;
  }
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    setStatus("Camera API unavailable");
    return;
  }
  if (!state.detector && "BarcodeDetector" in window) {
    await updateSupportStatus();
  }
  if (!state.detector && !window.jsQR) {
    setStatus("No browser QR decoder");
  }

  stopCamera();
  try {
    const deviceId = els.cameraSelect.value;
    const video = deviceId
      ? { deviceId: { exact: deviceId }, width: { ideal: 1920 }, height: { ideal: 1080 }, frameRate: { ideal: 60 } }
      : { facingMode: { ideal: "environment" }, width: { ideal: 1920 }, height: { ideal: 1080 }, frameRate: { ideal: 60 } };

    state.stream = await navigator.mediaDevices.getUserMedia({ audio: false, video });
    await tuneCameraTrack(state.stream);
    els.video.srcObject = state.stream;
    await els.video.play();
    state.running = true;
    els.startButton.disabled = true;
    els.stopButton.disabled = false;
    await populateCameras();
    setStatus(state.detector || window.jsQR ? "Scanning" : "Camera open");
    updateUi();
    startRateMeter();
    startClock();
    scanLoop();
  } catch (error) {
    setStatus(cameraErrorMessage(error));
    stopCamera();
  }
}

function stopCamera() {
  state.running = false;
  stopRateMeter();
  stopClock();
  if (state.stream) {
    for (const track of state.stream.getTracks()) {
      track.stop();
    }
  }
  state.stream = null;
  els.video.srcObject = null;
  els.startButton.disabled = false;
  els.stopButton.disabled = true;
  updateUi();
}

async function restartCamera() {
  if (state.running) {
    await startCamera();
  }
}

async function populateCameras() {
  if (!navigator.mediaDevices?.enumerateDevices) {
    return;
  }
  const current = els.cameraSelect.value;
  const devices = await navigator.mediaDevices.enumerateDevices();
  const cameras = devices.filter((device) => device.kind === "videoinput");
  els.cameraSelect.innerHTML = "";
  for (const [index, camera] of cameras.entries()) {
    const option = document.createElement("option");
    option.value = camera.deviceId;
    option.textContent = camera.label || `Camera ${index + 1}`;
    els.cameraSelect.append(option);
  }
  if (current && cameras.some((camera) => camera.deviceId === current)) {
    els.cameraSelect.value = current;
    return;
  }
  const backId = backCameraId(cameras);
  if (backId) {
    els.cameraSelect.value = backId;
  }
}

function backCameraId(cameras) {
  // The stream is opened with facingMode "environment", so the active track
  // already points at the back camera — match the dropdown to it exactly.
  const activeId = state.stream?.getVideoTracks?.()[0]?.getSettings?.().deviceId;
  if (activeId && cameras.some((camera) => camera.deviceId === activeId)) {
    return activeId;
  }
  // No active track yet: fall back to a label heuristic.
  const back = cameras.find((camera) => /\b(back|rear|environment)\b/i.test(camera.label));
  return back ? back.deviceId : "";
}

async function tuneCameraTrack(stream) {
  const [track] = stream.getVideoTracks();
  if (!track || !track.getCapabilities || !track.applyConstraints) {
    return;
  }
  const caps = track.getCapabilities();
  const advanced = {};
  if (Array.isArray(caps.focusMode) && caps.focusMode.includes("continuous")) {
    advanced.focusMode = "continuous";
  }
  if (Array.isArray(caps.exposureMode) && caps.exposureMode.includes("continuous")) {
    advanced.exposureMode = "continuous";
  }
  if (Array.isArray(caps.whiteBalanceMode) && caps.whiteBalanceMode.includes("continuous")) {
    advanced.whiteBalanceMode = "continuous";
  }
  if (typeof caps.zoom?.max === "number" && typeof caps.zoom?.min === "number" && caps.zoom.max > caps.zoom.min) {
    advanced.zoom = Math.min(caps.zoom.max, Math.max(caps.zoom.min, 1.3));
  }
  if (!Object.keys(advanced).length) {
    return;
  }
  try {
    await track.applyConstraints({ advanced: [advanced] });
  } catch (error) {
    // Camera tuning is opportunistic; unsupported constraints should not block scanning.
  }
}

function startRateMeter() {
  stopRateMeter();
  state.scanCount = 0;
  state.rateBaseline = 0;
  state.rateTimer = window.setInterval(() => {
    const decodes = state.scanCount - state.rateBaseline;
    state.rateBaseline = state.scanCount;
    els.rateValue.textContent = `${Math.round(decodes * (1000 / RATE_WINDOW_MS))}`;
  }, RATE_WINDOW_MS);
}

function stopRateMeter() {
  if (state.rateTimer) {
    window.clearInterval(state.rateTimer);
    state.rateTimer = null;
  }
  els.rateValue.textContent = "0";
}

function startClock() {
  stopClock();
  // Refresh elapsed/ETA once a second so the clock advances even when no new
  // frame has been decoded yet.
  state.clockTimer = window.setInterval(updateTiming, 1000);
}

function stopClock() {
  if (state.clockTimer) {
    window.clearInterval(state.clockTimer);
    state.clockTimer = null;
  }
}

function updateTiming() {
  const meta = state.metadata;
  const total = meta?.total || 0;
  const count = meta?.fountain ? state.fountainRank : state.frames.size;
  const left = total ? Math.max(0, total - count) : 0;
  const elapsedMs = state.transferStartAt ? Date.now() - state.transferStartAt : 0;

  const elapsed = formatDuration(elapsedMs);
  els.elapsedValue.textContent = elapsed;
  els.detailElapsed.textContent = state.transferStartAt ? elapsed : "—";

  let eta;
  if (state.completed || (total && left === 0)) {
    eta = "done";
  } else if (count > 0 && left > 0 && elapsedMs > 750) {
    // Adaptive: project the average pace so far (elapsed per captured frame)
    // across the frames still missing. Naturally self-corrects as it runs.
    eta = `~${formatDuration((elapsedMs / count) * left)} left`;
  } else {
    eta = "—";
  }
  els.etaValue.textContent = eta;
  els.detailEta.textContent = eta === "—" ? "—" : eta.replace(/^~|\s*left$/g, "");
}

function formatDuration(ms) {
  const totalSeconds = Math.max(0, Math.round(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  }
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

async function scanLoop() {
  if (!state.running || state.scanning) {
    return;
  }
  state.scanning = true;
  try {
    if ((state.detector || window.jsQR) && els.video.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA) {
      const codes = await detectCodes();
      state.scanCount++;
      for (const code of codes) {
        const payload = code.rawValue || "";
        if (payload) {
          await addPayload(payload);
        }
      }
    }
  } catch (error) {
    setStatus(`Scan error: ${error.message}`);
  } finally {
    state.scanning = false;
    if (state.running) {
      // A small jittered delay keeps decoding near the camera's frame rate while
      // avoiding phase-lock with the sender's animation, which would otherwise
      // keep missing the same frame.
      window.setTimeout(scanLoop, 8 + Math.floor(Math.random() * 12));
    }
  }
}

async function detectCodes() {
  if (state.detector) {
    try {
      state.lastCanvasFallback = false;
      // A successful native detect is authoritative, including an empty result.
      // Never fall through to jsQR here, or the slow path runs on every frame
      // that has no QR in view and throttles the whole scan loop.
      return await state.detector.detect(els.video);
    } catch (error) {
      // Some engines cannot read a <video> directly; retry once through a canvas.
      state.lastCanvasFallback = true;
      const canvas = drawVideoToCanvas();
      if (canvas) {
        try {
          return await state.detector.detect(canvas);
        } catch (canvasError) {
          // Native detection is unusable on this engine; fall back to jsQR.
        }
      }
    }
  }
  return detectWithJsQR();
}

function drawVideoToCanvas(options = {}) {
  const canvas = els.canvas;
  const width = els.video.videoWidth;
  const height = els.video.videoHeight;
  if (!width || !height) {
    return null;
  }
  let sourceX = 0;
  let sourceY = 0;
  let sourceWidth = width;
  let sourceHeight = height;
  if (options.cropRatio) {
    const side = Math.floor(Math.min(width, height) * options.cropRatio);
    sourceX = Math.floor((width - side) / 2);
    sourceY = Math.floor((height - side) / 2);
    sourceWidth = side;
    sourceHeight = side;
  }

  const maxSide = options.maxSide || 720;
  const scale = Math.min(1, maxSide / Math.max(sourceWidth, sourceHeight));
  canvas.width = Math.max(1, Math.floor(sourceWidth * scale));
  canvas.height = Math.max(1, Math.floor(sourceHeight * scale));
  canvas.getContext("2d", { willReadFrequently: true }).drawImage(
    els.video,
    sourceX,
    sourceY,
    sourceWidth,
    sourceHeight,
    0,
    0,
    canvas.width,
    canvas.height,
  );
  return canvas;
}

function detectWithJsQR() {
  if (!window.jsQR) {
    return [];
  }
  return detectWithJsQRCanvas({ cropRatio: 0.72, maxSide: 1280, inversionAttempts: "dontInvert" }) ||
    detectWithJsQRCanvas({ cropRatio: 0.86, maxSide: 1440, inversionAttempts: "dontInvert" }) ||
    detectWithJsQRCanvas({ maxSide: 1440, inversionAttempts: "dontInvert" }) ||
    detectWithJsQRCanvas({ cropRatio: 0.86, maxSide: 1440, inversionAttempts: "attemptBoth" }) ||
    [];
}

function detectWithJsQRCanvas(options) {
  const canvas = drawVideoToCanvas(options);
  if (!canvas) {
    return null;
  }
  const context = canvas.getContext("2d", { willReadFrequently: true });
  const image = context.getImageData(0, 0, canvas.width, canvas.height);
  const code = window.jsQR(image.data, image.width, image.height, { inversionAttempts: options.inversionAttempts || "dontInvert" });
  return code?.data ? [{ rawValue: code.data }] : null;
}

async function addPayload(rawPayload) {
  const payload = rawPayload.trim();
  if (!payload) {
    return;
  }

  const now = Date.now();
  if (payload === state.lastPayload && now - state.lastPayloadAt < 350) {
    return;
  }
  state.lastPayload = payload;
  state.lastPayloadAt = now;

  if (payload.startsWith("AIRQR2|")) {
    await handleFountainFrame(payload);
    return;
  }

  if (!payload.startsWith("AIRQR1|")) {
    state.completed = true;
    els.resultText.value = payload;
    els.copyButton.disabled = false;
    els.saveButton.disabled = false;
    setStatus("Plain QR captured");
    stopCamera();
    updateUi();
    return;
  }

  let frame;
  try {
    frame = parseFrame(payload);
  } catch (error) {
    setStatus(error.message);
    return;
  }

  if (state.metadata && frame.session !== state.metadata.session) {
    clearTransferState();
  }
  if (!state.metadata) {
    state.metadata = {
      session: frame.session,
      total: frame.total,
      flags: frame.flags,
      originalSize: frame.originalSize,
      sha256: frame.sha256,
    };
  }

  const mismatch = metadataMismatch(frame);
  if (mismatch) {
    setStatus(mismatch);
    return;
  }

  if (!state.frames.has(frame.index)) {
    state.frames.set(frame.index, frame.data);
    state.lastFrameIndex = frame.index;
    state.lastFrameAt = Date.now();
    setStatus(`Frame ${frame.index} captured`);
    updateUi();
  } else {
    state.lastFrameIndex = frame.index;
    state.lastFrameAt = Date.now();
    updateUi();
  }

  if (!state.completed && state.frames.size === state.metadata.total) {
    await assembleTransfer();
  }
}

function parseFrame(payload) {
  const parts = payload.split("|");
  if (parts.length !== 8) {
    throw new Error("Invalid AirQR frame");
  }
  const [prefix, session, indexText, totalText, flags, sizeText, sha256, chunkText] = parts;
  if (prefix !== "AIRQR1") {
    throw new Error("Unsupported AirQR version");
  }
  const index = Number.parseInt(indexText, 10);
  const total = Number.parseInt(totalText, 10);
  const originalSize = Number.parseInt(sizeText, 10);
  if (!Number.isInteger(index) || index <= 0) {
    throw new Error("Invalid frame index");
  }
  if (!Number.isInteger(total) || total <= 0 || index > total) {
    throw new Error("Invalid frame total");
  }
  if (flags !== "z" && flags !== "n") {
    throw new Error("Unsupported AirQR flags");
  }
  if (!Number.isInteger(originalSize) || originalSize < 0) {
    throw new Error("Invalid transfer size");
  }
  if (!/^[0-9a-f]{64}$/i.test(sha256)) {
    throw new Error("Invalid transfer hash");
  }
  return {
    session,
    index,
    total,
    flags,
    originalSize,
    sha256: sha256.toLowerCase(),
    data: base64UrlToBytes(chunkText),
  };
}

function metadataMismatch(frame) {
  const meta = state.metadata;
  if (!meta) {
    return "";
  }
  if (frame.total !== meta.total) {
    return "Frame count mismatch";
  }
  if (frame.flags !== meta.flags) {
    return "Compression flag mismatch";
  }
  if (frame.originalSize !== meta.originalSize) {
    return "Size mismatch";
  }
  if (frame.sha256 !== meta.sha256) {
    return "Hash mismatch";
  }
  return "";
}

async function assembleTransfer() {
  state.assembling = true;
  setStatus("Verifying");
  updateUi();

  try {
    const meta = state.metadata;
    const chunks = [];
    let packedLength = 0;
    for (let index = 1; index <= meta.total; index++) {
      const chunk = state.frames.get(index);
      if (!chunk) {
        throw new Error(`Missing frame ${index}`);
      }
      chunks.push(chunk);
      packedLength += chunk.length;
    }

    let bytes = concatBytes(chunks, packedLength);
    if (meta.flags === "z") {
      bytes = await decompressGzip(bytes);
    }
    if (bytes.length !== meta.originalSize) {
      throw new Error(`Size check failed: ${bytes.length} / ${meta.originalSize}`);
    }
    const hash = await sha256Hex(bytes);
    if (hash !== meta.sha256) {
      throw new Error("Hash check failed");
    }

    els.resultText.value = new TextDecoder().decode(bytes);
    els.copyButton.disabled = false;
    els.saveButton.disabled = false;
    state.completed = true;
    setStatus("Complete");
    // The transfer is finished; power the camera down until the user scans again.
    stopCamera();
  } catch (error) {
    setStatus(error.message);
  } finally {
    state.assembling = false;
    updateUi();
  }
}

async function handleFountainFrame(payload) {
  if (!window.AirQRFountain) {
    setStatus("Fountain decoder unavailable");
    return;
  }
  let frame;
  try {
    frame = parseFountainFrame(payload);
  } catch (error) {
    setStatus(error.message);
    return;
  }

  // A new session (or switching from a legacy AIRQR1 capture) starts fresh.
  if (state.metadata && (!state.metadata.fountain || state.metadata.session !== frame.session)) {
    clearTransferState();
  }
  if (!state.fountain) {
    state.metadata = {
      session: frame.session,
      total: frame.K,
      flags: frame.flags,
      originalSize: frame.originalSize,
      sha256: frame.sha256,
      fountain: true,
      symbolSize: frame.T,
      transferSize: frame.transferSize,
    };
    state.fountain = new window.AirQRFountain.Decoder(frame.K, frame.T);
  }

  const mismatch = fountainMismatch(frame);
  if (mismatch) {
    setStatus(mismatch);
    return;
  }

  state.fountainEsi = frame.esi;
  state.lastFrameAt = Date.now();
  let done;
  try {
    done = state.fountain.add(frame.esi, frame.data);
  } catch (error) {
    setStatus(error.message);
    return;
  }
  state.fountainRank = state.fountain.rank;
  if (!state.completed) {
    setStatus(`Symbol ${frame.esi} · ${state.fountain.rank}/${frame.K}`);
  }

  if (done && !state.completed) {
    await finishFountain(frame);
  }
  updateUi();
}

async function finishFountain(frame) {
  try {
    const packed = state.fountain.packed();
    if (!packed) {
      throw new Error("Decoder incomplete");
    }
    let bytes = packed.subarray(0, frame.transferSize);
    if (frame.flags === "z") {
      bytes = await decompressGzip(bytes);
    }
    if (bytes.length !== frame.originalSize) {
      throw new Error(`Size check failed: ${bytes.length} / ${frame.originalSize}`);
    }
    const hash = await sha256Hex(bytes);
    if (hash !== frame.sha256) {
      throw new Error("Hash check failed");
    }
    els.resultText.value = new TextDecoder().decode(bytes);
    els.copyButton.disabled = false;
    els.saveButton.disabled = false;
    state.completed = true;
    setStatus("Complete");
    // Transfer finished; power the camera down until the user scans again.
    stopCamera();
  } catch (error) {
    setStatus(error.message);
  }
}

function fountainMismatch(frame) {
  const meta = state.metadata;
  if (frame.K !== meta.total) {
    return "Symbol count mismatch";
  }
  if (frame.T !== meta.symbolSize) {
    return "Symbol size mismatch";
  }
  if (frame.flags !== meta.flags) {
    return "Compression flag mismatch";
  }
  if (frame.originalSize !== meta.originalSize) {
    return "Size mismatch";
  }
  if (frame.transferSize !== meta.transferSize) {
    return "Transfer size mismatch";
  }
  if (frame.sha256 !== meta.sha256) {
    return "Hash mismatch";
  }
  return "";
}

function parseFountainFrame(payload) {
  const parts = payload.split("|");
  if (parts.length !== 10) {
    throw new Error("Invalid AirQR2 frame");
  }
  const [prefix, session, esiText, kText, tText, flags, transferText, sizeText, sha256, dataText] = parts;
  if (prefix !== "AIRQR2") {
    throw new Error("Unsupported AirQR version");
  }
  const esi = Number.parseInt(esiText, 10);
  const K = Number.parseInt(kText, 10);
  const T = Number.parseInt(tText, 10);
  const transferSize = Number.parseInt(transferText, 10);
  const originalSize = Number.parseInt(sizeText, 10);
  if (!Number.isInteger(esi) || esi < 0) {
    throw new Error("Invalid symbol id");
  }
  if (!Number.isInteger(K) || K <= 0) {
    throw new Error("Invalid symbol count");
  }
  if (!Number.isInteger(T) || T <= 0) {
    throw new Error("Invalid symbol size");
  }
  if (flags !== "z" && flags !== "n") {
    throw new Error("Unsupported AirQR flags");
  }
  if (!Number.isInteger(transferSize) || transferSize < 0 || !Number.isInteger(originalSize) || originalSize < 0) {
    throw new Error("Invalid transfer size");
  }
  if (!/^[0-9a-f]{64}$/i.test(sha256)) {
    throw new Error("Invalid transfer hash");
  }
  const data = base64UrlToBytes(dataText);
  if (data.length !== T) {
    throw new Error("Symbol length mismatch");
  }
  return { session, esi, K, T, flags, transferSize, originalSize, sha256: sha256.toLowerCase(), data };
}

function resetTransfer() {
  clearTransferState();
  els.resultText.value = "";
  els.manualFrames.value = "";
  els.copyButton.disabled = true;
  els.copyButton.textContent = "Copy text";
  els.saveButton.disabled = true;
  setStatus(state.running ? "Scanning" : "Idle");
  updateUi();
}

function clearTransferState() {
  state.session = "";
  state.metadata = null;
  state.frames.clear();
  state.assembling = false;
  state.completed = false;
  state.lastPayload = "";
  state.lastPayloadAt = 0;
  state.lastFrameIndex = 0;
  state.lastFrameAt = 0;
  state.fountain = null;
  state.fountainRank = 0;
  state.fountainEsi = null;
  state.transferStartAt = 0;
}

function updateUi() {
  const meta = state.metadata;
  const fountain = !!meta?.fountain;
  const total = meta?.total || 0;
  const count = fountain ? state.fountainRank : state.frames.size;
  // Start the transfer clock on the first decoded frame.
  if (count > 0 && !state.transferStartAt) {
    state.transferStartAt = Date.now();
  }
  const decoderLabel = fountain ? "fountain" : state.decoderName || "decoder";
  const mode = meta ? `${meta.flags === "z" ? "gzip" : "plain"} / ${decoderLabel}` : "—";
  els.frameCount.textContent = String(count).padStart(2, "0");
  els.frameTotal.textContent = total;
  els.remainingText.textContent = remainingText(total, count, fountain);
  els.sessionValue.textContent = meta?.session || "--------";
  els.detailSession.textContent = meta?.session || "—";
  els.modeValue.textContent = mode;
  els.lastFrameValue.textContent = lastFrameText(total, fountain);
  els.missingValue.textContent = missingFramesText(total, count, fountain);
  els.progressBar.style.width = total ? `${Math.round((count / total) * 100)}%` : "0";
  renderFrameDots(total, count, fountain);
  updatePhase(total, count, mode);
  updateTiming();
}

function remainingText(total, count, fountain) {
  if (!total) {
    return "Awaiting first frame";
  }
  const remaining = total - count;
  if (remaining <= 0) {
    return "All frames captured";
  }
  if (fountain) {
    return `${remaining} more symbol${remaining === 1 ? "" : "s"} needed`;
  }
  return `${remaining} frame${remaining === 1 ? "" : "s"} remaining`;
}

function lastFrameText(total, fountain) {
  if (fountain) {
    return state.fountainEsi === null ? "—" : `ESI ${state.fountainEsi}`;
  }
  return state.lastFrameIndex ? `${state.lastFrameIndex} / ${total}` : "—";
}

function updatePhase(total, count, mode) {
  const phase = state.completed ? "complete" : state.running ? "scanning" : "idle";
  els.app.dataset.phase = phase;
  els.phaseLabel.textContent = phase === "scanning" ? "CAPTURING" : phase === "complete" ? "DONE" : "STANDBY";
  els.startButton.textContent = state.completed ? "Scan again" : "Start scanning";
  els.resultSheet.hidden = phase !== "complete";
  if (phase === "complete") {
    els.resultSubtitle.textContent = `${total || count} frames · ${mode}`;
  }
}

function missingFramesText(total, count, fountain) {
  if (!total) {
    return "-";
  }
  if (fountain) {
    // Rateless: there are no specific missing indices, only a symbol shortfall.
    const remaining = total - count;
    return remaining > 0 ? `${remaining} more symbol${remaining === 1 ? "" : "s"}` : "none";
  }
  const missing = [];
  for (let index = 1; index <= total; index++) {
    if (!state.frames.has(index)) {
      missing.push(index);
      if (missing.length >= 8) {
        break;
      }
    }
  }
  if (!missing.length) {
    return "none";
  }
  const suffix = total - state.frames.size > missing.length ? "..." : "";
  return `${missing.join(", ")}${suffix}`;
}

function renderFrameDots(total, count, fountain) {
  els.frameDots.innerHTML = "";
  if (!total || total > 180) {
    return;
  }
  const fragment = document.createDocumentFragment();
  for (let index = 1; index <= total; index++) {
    const dot = document.createElement("span");
    if (fountain) {
      // No per-index identity; fill a progress meter of `count` cells, with the
      // newest pivot ringed.
      if (index <= count) {
        dot.className = index === count ? "seen last" : "seen";
      }
    } else if (state.frames.has(index)) {
      dot.className = index === state.lastFrameIndex ? "seen last" : "seen";
    }
    fragment.append(dot);
  }
  els.frameDots.append(fragment);
}

function addManualFrames() {
  const lines = els.manualFrames.value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  Promise.all(lines.map((line) => addPayload(line))).catch((error) => {
    setStatus(error.message);
  });
}

async function copyResult() {
  try {
    await navigator.clipboard.writeText(els.resultText.value);
    flashCopied();
    setStatus("Copied");
  } catch (error) {
    els.resultText.focus();
    els.resultText.select();
    setStatus("Select and copy manually");
  }
}

function flashCopied() {
  els.copyButton.textContent = "Copied ✓";
  window.clearTimeout(state.copyTimer);
  state.copyTimer = window.setTimeout(() => {
    els.copyButton.textContent = "Copy text";
  }, 1400);
}

function saveResult() {
  const blob = new Blob([els.resultText.value], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = "airqr.txt";
  document.body.append(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

function setStatus(text) {
  els.statusText.textContent = text;
  els.detailStatus.textContent = text;
}

function base64UrlToBytes(text) {
  const padded = text + "=".repeat((4 - (text.length % 4)) % 4);
  const base64 = padded.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function concatBytes(chunks, length) {
  const result = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.length;
  }
  return result;
}

async function decompressGzip(bytes) {
  if ("DecompressionStream" in window) {
    const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  }
  if (window.pako?.inflate) {
    return window.pako.inflate(bytes);
  }
  throw new Error("Gzip unsupported; send with --no-compress");
}

async function sha256Hex(bytes) {
  if (!crypto.subtle) {
    throw new Error("SHA-256 unavailable in this context");
  }
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

function cameraErrorMessage(error) {
  if (error.name === "NotAllowedError") {
    return "Camera permission denied";
  }
  if (error.name === "NotFoundError") {
    return "No camera found";
  }
  if (error.name === "NotReadableError") {
    return "Camera is busy";
  }
  return error.message || "Camera error";
}

function populateAccessInfo() {
  els.accessUrl.value = window.location.href;
  if (!isSecureContext) {
    els.networkHint.textContent = "Camera access needs HTTPS, except on localhost.";
  } else if (location.hostname === "127.0.0.1" || location.hostname === "localhost") {
    els.networkHint.textContent = "Localhost is ready for desktop testing.";
  } else {
    els.networkHint.textContent = "This address is ready if the phone can reach it on the network.";
  }
}

async function copyAccessUrl() {
  try {
    await navigator.clipboard.writeText(els.accessUrl.value);
    setStatus("URL copied");
  } catch (error) {
    els.accessUrl.focus();
    els.accessUrl.select();
    setStatus("Select and copy URL manually");
  }
}
