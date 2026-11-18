"use strict";

// Labels for the Files dock's breadcrumb row: the path from the tree's root, the
// "N min ago" of a time, and the "Edited by <session> · N min ago" on the right.
// Pure: no DOM, and the one function it takes from another script (drawerfiles.js
// changedSegs, the path normalizer behind the tree's session bars) is pure too,
// so `node --test` reaches it; filesdrawer.js draws what these return. Classic
// script: browser globals, required under node.

// crumbsFor: the breadcrumbs of `path` as [{name, path}], each with the absolute
// path a click reveals. Under `root` the first crumb is the root itself (its
// basename) and the rest are the segments below it; a path equal to the root is
// just that crumb. A path outside the root, or any path when there is no root,
// runs from "/" (a crumb named "/"), so a file the tree does not show reads as
// the absolute path it is. The same holds for a root of "/". Segments are taken
// whole and verbatim, so spaces, unicode and characters like # or ? survive; the
// compare against the root is by segment, so /h/proj2 is not inside /h/proj. A
// path that is not absolute has no crumbs: a relative one would need a base this
// function does not have, and guessing one would make a click reveal the wrong
// directory.
function crumbsFor(path, root) {
  const segsOf = typeof changedSegs === "function" ? changedSegs : require("./drawerfiles.js").changedSegs;
  const segs = segsOf(path);
  if (!segs) return [];
  const rsegs = typeof root === "string" && root !== "" ? segsOf(root) : null;
  const inside = rsegs !== null && rsegs.length <= segs.length && rsegs.every((s, i) => segs[i] === s);
  const from = inside ? rsegs.length : 0;
  const at = (n) => "/" + segs.slice(0, n).join("/");
  // The anchor: the root's own name, or "/" when there is nothing above the first segment.
  const out = [from > 0 ? { name: segs[from - 1], path: at(from) } : { name: "/", path: "/" }];
  for (let n = from + 1; n <= segs.length; n++) out.push({ name: segs[n - 1], path: at(n) });
  return out;
}

// relTime: how long ago `ms` was at `now` (both unix ms), in the largest whole
// unit: "just now" under a minute, "N min ago", "N h ago", "N d ago". A time a
// little ahead of ours (two clocks) is "just now"; a time or a now that is not a
// number is "".
function relTime(ms, now) {
  if (!Number.isFinite(ms) || !Number.isFinite(now)) return "";
  const s = Math.floor((now - ms) / 1000);
  if (s < 60) return "just now";
  const min = Math.floor(s / 60);
  if (min < 60) return min + " min ago";
  const h = Math.floor(min / 60);
  if (h < 24) return h + " h ago";
  return Math.floor(h / 24) + " d ago";
}

// editedLabel: the right side of the breadcrumb row. `files` is the session's
// Activity files ([{path, add, del, ts?}]); a file Activity names gives "Edited
// by <sessionName> · <relTime of its ts>", or "Edited by <sessionName>" when it
// has no usable ts (an older session, or one Activity could not time), and a
// file it does not name gives "" so the row shows nothing. So does a missing or
// blank sessionName: no label beats "Edited by undefined".
function editedLabel(files, path, sessionName, now) {
  if (typeof sessionName !== "string" || sessionName.trim() === "" || !Array.isArray(files)) return "";
  const segsOf = typeof changedSegs === "function" ? changedSegs : require("./drawerfiles.js").changedSegs;
  const want = segsOf(path);
  if (!want) return "";
  const key = want.join("/");
  for (const f of files) {
    const segs = f ? segsOf(f.path) : null;
    if (!segs || segs.join("/") !== key) continue;
    const label = "Edited by " + sessionName;
    const ago = Number.isFinite(f.ts) && f.ts > 0 ? relTime(f.ts, now) : "";
    return ago ? label + " · " + ago : label;
  }
  return "";
}

// crumbReveal: what a crumb click does to the tree. "root" is the tree's own root (the
// column opens and shows its top), "inside" is a folder below it (ftReveal opens down
// to it), "outside" is somewhere the tree does not show: the "/" crumb of a file
// outside the root, a parent of the root, a sibling that only shares its prefix. The
// dock never re-roots the tree from a crumb (it would move the user's tree under
// them), so "outside" is a plain refusal, never an error. A path that is not
// absolute, or no root, is "outside" too.
function crumbReveal(path, root) {
  const segsOf = typeof changedSegs === "function" ? changedSegs : require("./drawerfiles.js").changedSegs;
  const segs = segsOf(path);
  const rsegs = typeof root === "string" && root !== "" ? segsOf(root) : null;
  if (!segs || !rsegs || rsegs.length > segs.length || !rsegs.every((s, i) => segs[i] === s)) return "outside";
  return rsegs.length === segs.length ? "root" : "inside";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { crumbsFor, relTime, editedLabel, crumbReveal };
}
