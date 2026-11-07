"use strict";

// Text colour for the environment chip (F79). env_color is operator config, so
// nothing guarantees white text is readable on it: a pale yellow "staging" chip
// with white letters is unreadable. Pick black or white, whichever has the higher
// WCAG 2.x contrast against the colour. Black and white cross over at a contrast
// of about 4.58:1, so the pick clears 4.5:1 for any opaque colour.
//
// Classic script: pure, no DOM. app.js reads the colour the browser resolved
// (getComputedStyle), so named colours, hex and hsl() all arrive as rgb().

// sRGB channel (0-255) to linear light, WCAG 2.x relative luminance.
function channelLum(v) {
  const c = v / 255;
  return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}

function relLuminance(rgb) {
  return 0.2126 * channelLum(rgb[0]) + 0.7152 * channelLum(rgb[1]) + 0.0722 * channelLum(rgb[2]);
}

// envTextColor: [r, g, b] -> "#000000" | "#ffffff". A tie goes to white.
function envTextColor(rgb) {
  const l = relLuminance(rgb);
  const onBlack = (l + 0.05) / 0.05;  // contrast of black text: (L + .05) / (0 + .05)
  const onWhite = 1.05 / (l + 0.05);  // contrast of white text: (1 + .05) / (L + .05)
  return onWhite >= onBlack ? "#ffffff" : "#000000";
}

const RGB_NUM = "(\\d{1,3}(?:\\.\\d+)?)";
const RGB_ALPHA = "(?:\\d*\\.?\\d+%?)";
const RGB_COMMA = new RegExp("^rgba?\\(\\s*" + RGB_NUM + "\\s*,\\s*" + RGB_NUM + "\\s*,\\s*" + RGB_NUM + "\\s*(?:,\\s*" + RGB_ALPHA + "\\s*)?\\)$", "i");
const RGB_SPACE = new RegExp("^rgba?\\(\\s*" + RGB_NUM + "\\s+" + RGB_NUM + "\\s+" + RGB_NUM + "\\s*(?:/\\s*" + RGB_ALPHA + "\\s*)?\\)$", "i");

// parseRgb: "rgb(r, g, b)" / "rgba(r, g, b, a)" as getComputedStyle returns them,
// and the space-separated "rgb(r g b / a)". The alpha is dropped. Anything else,
// or a channel over 255, is null.
function parseRgb(css) {
  if (typeof css !== "string") return null;
  const s = css.trim();
  const m = RGB_COMMA.exec(s) || RGB_SPACE.exec(s);
  if (!m) return null;
  const rgb = [Number(m[1]), Number(m[2]), Number(m[3])];
  if (rgb.some((v) => v > 255)) return null;
  return rgb.map(Math.round);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { envTextColor, parseRgb };
}
