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

const { rewriteLocalhost } = require("../static/links.js");

test("localhost forms are rewritten to the host the console is viewed on", () => {
  for (const h of ["localhost", "127.0.0.1", "0.0.0.0"]) {
    assert.strictEqual(
      rewriteLocalhost("http://" + h + ":3030/deck?x=1#s", "100.76.118.121"),
      "http://100.76.118.121:3030/deck?x=1#s",
    );
  }
});

test("ipv6 loopback is rewritten too", () => {
  assert.strictEqual(
    rewriteLocalhost("http://[::1]:9000/a", "100.76.118.121"),
    "http://100.76.118.121:9000/a",
  );
});

test("a non-local host is returned byte-identical", () => {
  const u = "https://culiops.dev/a?b=1#c";
  assert.strictEqual(rewriteLocalhost(u, "100.76.118.121"), u);
});

test("no rewrite when the console itself is viewed at localhost", () => {
  const u = "http://localhost:3030/deck";
  assert.strictEqual(rewriteLocalhost(u, "localhost"), u);
});

test("unparseable input is returned unchanged", () => {
  assert.strictEqual(rewriteLocalhost("http://", "100.76.118.121"), "http://");
  assert.strictEqual(rewriteLocalhost("not a url", "100.76.118.121"), "not a url");
});

// Documents URL.toString() normalisation on the rewrite path. It only ever fires
// when a rewrite actually happened (a non-local host returns early, byte-identical),
// so it cannot cause a spurious "opened X instead of localhost" toast.
test("rewriting also normalises, and that is accepted", () => {
  assert.strictEqual(rewriteLocalhost("http://localhost:3030", "1.2.3.4"), "http://1.2.3.4:3030/");
  assert.strictEqual(rewriteLocalhost("http://localhost:80/a", "1.2.3.4"), "http://1.2.3.4/a");
});
