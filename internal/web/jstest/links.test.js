"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { scanLinks } = require("../static/links.js");

const kinds = (s) => scanLinks(s).map((t) => t.kind + ":" + t.text);

test("a URL yields one url token and no path token inside it", () => {
  assert.deepStrictEqual(
    kinds("see https://culiops.dev/learn/x.html now"),
    ["url:https://culiops.dev/learn/x.html"],
  );
});

test("a hyphenated host and path survive intact", () => {
  assert.deepStrictEqual(
    kinds("https://my-site.dev/a-b/c-d.html"),
    ["url:https://my-site.dev/a-b/c-d.html"],
  );
});

test("trailing sentence punctuation is not part of the URL", () => {
  assert.deepStrictEqual(kinds("go to https://culiops.dev/a."), ["url:https://culiops.dev/a"]);
  assert.deepStrictEqual(kinds("(see https://culiops.dev/a)."), ["url:https://culiops.dev/a"]);
});

test("a closing paren that belongs to the URL is kept", () => {
  assert.deepStrictEqual(
    kinds("https://en.wikipedia.org/wiki/Foo_(bar)"),
    ["url:https://en.wikipedia.org/wiki/Foo_(bar)"],
  );
});

// Asserts the absence of a URL token, not the absence of any token: PATH_RE is
// deliberately unchanged, so `ftp://x.dev/a.txt` still yields the PATH token
// `//x.dev/a.txt`. That is pre-existing behaviour and it is contained server-side
// by containedPath() in fsapi.go, which refuses anything outside $HOME.
test("no non-http scheme is ever tokenized as a url", () => {
  for (const s of [
    "javascript:alert(1)",
    "javascript:alert(1)//x.dev/a.html",
    "data:text/html,<b>x</b>",
    "file:///etc/passwd",
    "file:///etc/passwd.bak",
    "ftp://x.dev/a.txt",
    "hxxps://x.dev/a.html",
  ]) {
    assert.ok(scanLinks(s).every((t) => t.kind !== "url"), "no url token for " + s);
  }
});

test("file paths still tokenize exactly as before", () => {
  assert.deepStrictEqual(kinds("edited /home/claude/a.go"), ["path:/home/claude/a.go"]);
  assert.deepStrictEqual(kinds("open ~/notes/x.md"), ["path:~/notes/x.md"]);
  assert.deepStrictEqual(kinds("see ./rel.txt"), ["path:./rel.txt"]);
  assert.deepStrictEqual(kinds("in internal/web/api.go"), ["path:internal/web/api.go"]);
});

test("a mixed line resolves every token, in order", () => {
  assert.deepStrictEqual(
    kinds("./a.go and https://culiops.dev/b.html and ~/c.md"),
    ["path:./a.go", "url:https://culiops.dev/b.html", "path:~/c.md"],
  );
});

test("tokens never overlap and are sorted by start", () => {
  const toks = scanLinks("./a.go and https://culiops.dev/b.html and ~/c.md");
  for (let i = 1; i < toks.length; i++) {
    assert.ok(toks[i - 1].start < toks[i].start, "sorted");
    assert.ok(toks[i - 1].end <= toks[i].start, "non-overlapping");
  }
});
