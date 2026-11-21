"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  dockKindOf, fileTypeLabel, fileTypeShort, fmtModified,
  ZOOM_STEPS, zoomStep, fitScale, wheelScale, zoomScroll,
} = require("../static/filekinds.js");

test("dockKindOf routes by extension, case aside", () => {
  for (const e of ["png", "jpg", "jpeg", "gif", "webp", "svg", "avif", "bmp", "ico", "PNG"]) assert.equal(dockKindOf(e), "image", e);
  for (const e of ["mp4", "webm", "WEBM"]) assert.equal(dockKindOf(e), "video", e);
  for (const e of ["mp3", "wav", "ogg"]) assert.equal(dockKindOf(e), "audio", e);
  assert.equal(dockKindOf("pdf"), "pdf");
  assert.equal(dockKindOf("PDF"), "pdf");
  for (const e of ["ipynb", "sqlite", "sqlite3", "db", "zip", "tar", "gz", "tgz", "7z", "rar", "xz", "bz2", "zst", "doc", "docx",
    "xls", "xlsx", "ppt", "pptx", "odt", "ods", "odp", "epub", "mobi", "azw3"]) assert.equal(dockKindOf(e), "card", e);
  // csv/tsv and html stay text, and so does anything unknown or extensionless
  for (const e of ["csv", "tsv", "html", "go", "md", "txt", "", "xyz"]) assert.equal(dockKindOf(e), "text", e);
  assert.equal(dockKindOf(undefined), "text");
});

test("fileTypeLabel names a type, falling back to '<EXT> file' or 'Binary file'", () => {
  const want = {
    pdf: "PDF document", sqlite: "SQLite database", sqlite3: "SQLite database", db: "SQLite database",
    zip: "ZIP archive", tar: "TAR archive", gz: "Gzip archive", tgz: "Gzip archive", "7z": "7-Zip archive",
    rar: "RAR archive", xz: "XZ archive", bz2: "Bzip2 archive", zst: "Zstandard archive",
    doc: "Word document", docx: "Word document", xls: "Excel workbook", xlsx: "Excel workbook",
    ppt: "PowerPoint presentation", pptx: "PowerPoint presentation", odt: "OpenDocument text",
    ods: "OpenDocument spreadsheet", odp: "OpenDocument presentation", ipynb: "Jupyter notebook",
    epub: "EPUB ebook", mobi: "Kindle ebook", azw3: "Kindle ebook",
  };
  for (const [e, l] of Object.entries(want)) assert.equal(fileTypeLabel(e, false), l, e);
  assert.equal(fileTypeLabel("ZIP", false), "ZIP archive");
  assert.equal(fileTypeLabel("dat", true), "DAT file");
  assert.equal(fileTypeLabel("xyz", false), "XYZ file");
  assert.equal(fileTypeLabel("", true), "Binary file");
  // a too-large extensionless text file is not "Binary file"
  assert.equal(fileTypeLabel("", false), "File");
  assert.equal(fileTypeLabel(undefined, false), "File");
  assert.equal(fileTypeLabel(undefined, true), "Binary file");
});

test("fileTypeShort is the upper-case extension, FILE when there is none", () => {
  assert.equal(fileTypeShort("png"), "PNG");
  assert.equal(fileTypeShort("Webm"), "WEBM");
  assert.equal(fileTypeShort(""), "FILE");
  assert.equal(fileTypeShort(undefined), "FILE");
});

test("fmtModified: HH:MM on the same local day, else YYYY-MM-DD HH:MM, zero-padded", () => {
  const at = (y, m, d, h, mi) => new Date(y, m - 1, d, h, mi).getTime();
  const now = at(2026, 11, 21, 15, 30);
  assert.equal(fmtModified(at(2026, 11, 21, 9, 5), now), "09:05");
  assert.equal(fmtModified(at(2026, 11, 21, 0, 0), now), "00:00");
  assert.equal(fmtModified(at(2026, 11, 20, 23, 59), now), "2026-11-20 23:59");
  assert.equal(fmtModified(at(2026, 3, 4, 7, 8), now), "2026-03-04 07:08");
  assert.equal(fmtModified(at(2025, 11, 21, 15, 30), now), "2025-11-21 15:30");
  // a modified time later today (clock skew) is still today
  assert.equal(fmtModified(at(2026, 11, 21, 23, 59), now), "23:59");
  // nothing known
  assert.equal(fmtModified(0, now), "");
  assert.equal(fmtModified(undefined, now), "");
});

test("ZOOM_STEPS are the spec's fractions, ascending", () => {
  assert.deepEqual(ZOOM_STEPS, [0.05, 0.1, 0.25, 0.33, 0.5, 0.67, 0.75, 1, 1.25, 1.5, 2, 3, 4, 6, 8]);
});

test("zoomStep goes to the next step strictly beyond the current scale, null at the ends", () => {
  assert.equal(zoomStep(1, 1), 1.25);
  assert.equal(zoomStep(1, -1), 0.75);
  assert.equal(zoomStep(0.19, 1), 0.25);
  assert.equal(zoomStep(0.19, -1), 0.1);
  assert.equal(zoomStep(0.04, -1), null);
  assert.equal(zoomStep(0.05, -1), null);
  assert.equal(zoomStep(0.05, 1), 0.1);
  assert.equal(zoomStep(8, 1), null);
  assert.equal(zoomStep(8, -1), 6);
  assert.equal(zoomStep(9, 1), null);
  assert.equal(zoomStep(9, -1), 8);
  // float noise around a step does not make it its own neighbour
  assert.equal(zoomStep(1.0000001, 1), 1.25);
  assert.equal(zoomStep(1.0000001, -1), 0.75);
  assert.equal(zoomStep(0.9999999, 1), 1.25);
  assert.equal(zoomStep(0.9999999, -1), 0.75);
  // walking up visits every step once
  const up = []; for (let s = 0.05, n = zoomStep(s, 1); n !== null; s = n, n = zoomStep(s, 1)) up.push(n);
  assert.deepEqual(up, ZOOM_STEPS.slice(1));
});

test("fitScale never enlarges and fits the tighter axis", () => {
  assert.equal(fitScale(100, 100, 500, 500), 1);
  assert.equal(fitScale(2000, 1000, 1000, 1000), 0.5);
  assert.equal(fitScale(1000, 2000, 1000, 500), 0.25);
  assert.equal(fitScale(0, 100, 500, 500), 1);
  assert.equal(fitScale(100, 0, 500, 500), 1);
  assert.equal(fitScale(0, 0, 0, 0), 1);
  // a stage measured while hidden (0 x 0) must never give scale 0 (NaN downstream)
  assert.equal(fitScale(100, 100, 0, 0), 1);
  assert.equal(fitScale(100, 100, 0, 500), 1);
  assert.equal(fitScale(100, 100, 500, 0), 1);
  assert.equal(fitScale(2000, 1000, -5, 800), 1);
});

test("wheelScale is cur * e^(-dy/300), line deltas x16, clamped to 5%..800%", () => {
  assert.equal(wheelScale(1, 0, 0), 1);
  assert.ok(Math.abs(wheelScale(1, -300, 0) - Math.E) < 1e-9);
  assert.ok(Math.abs(wheelScale(2, 300, 0) - 2 / Math.E) < 1e-9);
  // one line-mode notch of 3 = 48 px
  assert.ok(Math.abs(wheelScale(1, -3, 1) - Math.exp(48 / 300)) < 1e-9);
  assert.ok(Math.abs(wheelScale(1, -3, 0) - Math.exp(3 / 300)) < 1e-9);
  assert.equal(wheelScale(7, -1000, 0), 8);
  assert.equal(wheelScale(0.06, 1000, 0), 0.05);
});

// The image's point under the pointer: u = (left + px - ox0) / s0, in natural pixels.
const under = (a, left, s, dim, stage, px) => (left + px - Math.max(0, (stage - dim * s) / 2)) / s;

test("zoomScroll keeps the image point under the pointer when zooming in and out", () => {
  const a = { stageW: 800, stageH: 600, nw: 2000, nh: 1500 };
  for (const [s0, s1] of [[0.8, 1.6], [1.6, 3.2], [1, 0.8], [0.8, 0.5]]) {
    const left = 300, top = 200, px = 250, py = 120;
    const r = zoomScroll({ ...a, s0, s1, left, top, px, py });
    // a valid scroll at s0 and no clamping at s1 in these cases, so the point is exactly preserved
    assert.ok(Math.abs(under(a, r.left, s1, a.nw, a.stageW, px) - under(a, left, s0, a.nw, a.stageW, px)) < 1e-6, `x ${s0}->${s1}`);
    assert.ok(Math.abs(under(a, r.top, s1, a.nh, a.stageH, py) - under(a, top, s0, a.nh, a.stageH, py)) < 1e-6, `y ${s0}->${s1}`);
  }
});

test("zoomScroll clamps at 0 and at the far edge", () => {
  const a = { stageW: 800, stageH: 600, nw: 2000, nh: 1500 };
  // zooming out near the left edge would want a negative offset
  assert.equal(zoomScroll({ ...a, s0: 1, s1: 0.5, left: 10, top: 10, px: 700, py: 500 }).left, 0);
  assert.equal(zoomScroll({ ...a, s0: 1, s1: 0.5, left: 10, top: 10, px: 700, py: 500 }).top, 0);
  // zooming in at the right edge of the view cannot scroll past the canvas end: 2000*2 - 800
  const r = zoomScroll({ ...a, s0: 1, s1: 2, left: 1200, top: 900, px: 800, py: 600 });
  assert.equal(r.left, 2000 * 2 - 800);
  assert.equal(r.top, 1500 * 2 - 600);
});

test("zoomScroll centres (offset 0) when the image is smaller than the stage after the zoom", () => {
  const a = { stageW: 800, stageH: 600, nw: 400, nh: 300 };
  assert.deepEqual(zoomScroll({ ...a, s0: 1, s1: 1.5, left: 0, top: 0, px: 100, py: 100 }), { left: 0, top: 0 });
  // zoom in from a centred small image to one that overflows: the point stays put
  const b = { stageW: 800, stageH: 600, nw: 1000, nh: 500 };
  const r = zoomScroll({ ...b, s0: 0.5, s1: 2, left: 0, top: 0, px: 400, py: 300 });
  // s0=.5: image 500x250 centred at ox=150, oy=175; point u=((0+400-150)/.5, (300-175)/.5)=(500,250)
  assert.equal(r.left, 500 * 2 - 400);
  assert.equal(r.top, 0 + Math.max(0, Math.min(250 * 2 + 0 - 300, 500 * 2 - 600)));
});

test("zoomScroll on a mixed case: wide image fits the width, tall after the zoom", () => {
  const r = zoomScroll({ stageW: 800, stageH: 600, nw: 800, nh: 2000, s0: 1, s1: 1, left: 0, top: 500, px: 400, py: 300 });
  assert.deepEqual(r, { left: 0, top: 500 });
});
