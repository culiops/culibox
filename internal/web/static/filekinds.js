"use strict";

// What KIND of file the Files dock is looking at, what to call it, and the
// arithmetic of the image zoom, with no DOM and no fetch, so the views that use
// them (dockfileview.js) stay thin and the rules are testable under
// `node --test`. The kind is decided from the extension alone: an image, a video
// or a PDF never goes through the text read.
//
// Loaded as a classic script: these top-level names are shared globals.
// IMAGE_EXTS is copyfile.js's (one list for the dock, the phone viewer and the
// image diff); under node it is required, in the page it is already global.

const imageExtsOf = () => (typeof IMAGE_EXTS !== "undefined" ? IMAGE_EXTS : require("./copyfile.js").IMAGE_EXTS);

const VIDEO_EXTS = new Set(["mp4", "webm"]);
const AUDIO_EXTS = new Set(["mp3", "wav", "ogg"]);
// Types with no viewer: they get the fallback card. A PDF is not here, it has its own card.
const CARD_EXTS = new Set(["ipynb", "sqlite", "sqlite3", "db", "zip", "tar", "gz", "tgz", "7z", "rar", "xz", "bz2", "zst",
  "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "odp", "epub", "mobi", "azw3"]);

const FILE_TYPE_LABELS = {
  pdf: "PDF document",
  sqlite: "SQLite database", sqlite3: "SQLite database", db: "SQLite database",
  zip: "ZIP archive", tar: "TAR archive", gz: "Gzip archive", tgz: "Gzip archive", "7z": "7-Zip archive",
  rar: "RAR archive", xz: "XZ archive", bz2: "Bzip2 archive", zst: "Zstandard archive",
  doc: "Word document", docx: "Word document", xls: "Excel workbook", xlsx: "Excel workbook",
  ppt: "PowerPoint presentation", pptx: "PowerPoint presentation",
  odt: "OpenDocument text", ods: "OpenDocument spreadsheet", odp: "OpenDocument presentation",
  ipynb: "Jupyter notebook", epub: "EPUB ebook", mobi: "Kindle ebook", azw3: "Kindle ebook",
};

const lowerExt = (ext) => String(ext || "").toLowerCase();

// dockKindOf: "image" | "video" | "audio" | "pdf" | "card" | "text". Anything not
// listed (csv, html, no extension, ...) is text: today's read decides from there
// whether it really is, or is binary / too large and gets the card after all.
function dockKindOf(ext) {
  const e = lowerExt(ext);
  if (imageExtsOf().has(e)) return "image";
  if (VIDEO_EXTS.has(e)) return "video";
  if (AUDIO_EXTS.has(e)) return "audio";
  if (e === "pdf") return "pdf";
  if (CARD_EXTS.has(e)) return "card";
  return "text";
}

// fileTypeLabel: the type in words for the card's meta line. `binary` only
// matters for the wording when there is nothing better: no extension at all.
function fileTypeLabel(ext, binary) {
  const e = lowerExt(ext);
  if (Object.prototype.hasOwnProperty.call(FILE_TYPE_LABELS, e)) return FILE_TYPE_LABELS[e];
  return e ? e.toUpperCase() + " file" : "Binary file";
}

// fileTypeShort: the status bar's type, FILE when the name has no extension.
function fileTypeShort(ext) {
  const e = lowerExt(ext);
  return e ? e.toUpperCase() : "FILE";
}

const zeroPad2 = (n) => (n < 10 ? "0" : "") + n;

// fmtModified: local time, HH:MM when `ms` falls on the same local day as `nowMs`,
// else YYYY-MM-DD HH:MM. "" when the time is unknown (0 or absent).
function fmtModified(ms, nowMs) {
  if (!ms) return "";
  const d = new Date(ms), n = new Date(nowMs);
  const hm = zeroPad2(d.getHours()) + ":" + zeroPad2(d.getMinutes());
  if (d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate()) return hm;
  return d.getFullYear() + "-" + zeroPad2(d.getMonth() + 1) + "-" + zeroPad2(d.getDate()) + " " + hm;
}

// The − / + steps, as scale fractions: 5 10 25 33 50 67 75 100 125 150 200 300 400 600 800 %.
const ZOOM_STEPS = [0.05, 0.1, 0.25, 0.33, 0.5, 0.67, 0.75, 1, 1.25, 1.5, 2, 3, 4, 6, 8];
const ZOOM_MIN = ZOOM_STEPS[0];
const ZOOM_MAX = ZOOM_STEPS[ZOOM_STEPS.length - 1];
const ZOOM_EPS = 1e-6;

// zoomStep: the next step strictly beyond `cur` in direction `dir` (+1 / -1), or
// null at the end. The epsilon keeps a scale that is a step plus float noise
// (1.0000001 after a wheel zoom) from stepping to itself.
function zoomStep(cur, dir) {
  if (dir > 0) {
    for (const s of ZOOM_STEPS) if (s > cur + ZOOM_EPS) return s;
    return null;
  }
  for (let i = ZOOM_STEPS.length - 1; i >= 0; i--) if (ZOOM_STEPS[i] < cur - ZOOM_EPS) return ZOOM_STEPS[i];
  return null;
}

// fitScale: the scale at which the image fits the stage, never above 1. An image
// with no intrinsic size (nw or nh 0, e.g. an SVG with no width) cannot be fitted: 1.
function fitScale(nw, nh, bw, bh) {
  if (!nw || !nh) return 1;
  return Math.min(1, bw / nw, bh / nh);
}

// wheelScale: Ctrl/Cmd + wheel zooms continuously. Line-mode deltas (Firefox's
// mouse wheel, deltaMode 1) are lines, not pixels, so x16.
function wheelScale(cur, deltaY, deltaMode) {
  const dy = deltaMode === 1 ? deltaY * 16 : deltaY;
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, cur * Math.exp(-dy / 300)));
}

// zoomScroll: the new scroll offset that keeps the image point under the pointer.
// The stage scrolls a canvas of max(image, stage) size with the image centred in
// it, so the image's offset in the canvas is ox = max(0, (stage - n*s) / 2) on each
// axis. (px, py) is the pointer in the stage's viewport; (left, top) the current scroll.
function zoomScroll({ stageW, stageH, nw, nh, s0, s1, left, top, px, py }) {
  const axis = (stage, n, scroll, p) => {
    const o0 = Math.max(0, (stage - n * s0) / 2);
    const o1 = Math.max(0, (stage - n * s1) / 2);
    const u = (scroll + p - o0) / s0;
    return Math.min(Math.max(0, n * s1 - stage), Math.max(0, u * s1 + o1 - p));
  };
  return { left: axis(stageW, nw, left, px), top: axis(stageH, nh, top, py) };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { dockKindOf, fileTypeLabel, fileTypeShort, fmtModified, ZOOM_STEPS, zoomStep, fitScale, wheelScale, zoomScroll };
}
