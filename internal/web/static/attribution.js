"use strict";

// Formatting for the context-by-tool attribution dialog (GET
// /api/sessions/{id}/attribution). The per-tool number is cumulative tool_result
// payload BYTES; we show it as approx tokens (÷4) since the whole feature is about
// context tokens. It's a proxy — the dialog states that. Null-safe, never throws.

// fmtCtx: bytes → "≈<approx tokens>" ("≈404k" / "≈2.0M"). "" for 0/absent.
function fmtCtx(bytes) {
  if (!bytes || bytes < 0) return "";
  const n = Math.round(bytes / 4); // ~4 bytes/token
  if (n < 1000) return "≈" + n;
  if (n < 1_000_000) return "≈" + Math.round(n / 1000) + "k";
  return "≈" + (n / 1_000_000).toFixed(1) + "M";
}

// attributionRows: server Attribution → display rows (already sorted server-side).
function attributionRows(attr) {
  if (!attr || !Array.isArray(attr.tools)) return [];
  return attr.tools.map((t) => ({
    name: t.name,
    kind: t.kind,
    calls: t.calls,
    ctx: fmtCtx(t.ctxBytes),
  }));
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { fmtCtx, attributionRows };
}
