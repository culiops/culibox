#!/usr/bin/env node
// agent-box gh wrapper. Runs inside a box (the /usr/local/bin/gh shim execs it).
// It asks the agent-box broker on the host for a short-lived GitHub installation
// token for the repo's owner, then runs the real gh with that token in GH_TOKEN
// for this one command only. The token is never in the session env. Never prints it.
//
// The transport below repeats the one in agentbox-cred.js on purpose: each script
// is a single file the box mounts, and an AWS-only binding's directory must stay
// exactly what it was.
'use strict';
const http = require('http');
const { spawn, spawnSync } = require('child_process');
const os = require('os');

const ep = process.env.AGENTBOX_BROKER || '';
const tok = process.env.AGENTBOX_BROKER_TOKEN || '';
// AGENTBOX_GH_REAL is the real gh's path. A box never sets it (the image's gh
// is the default); a Host session sets it to the gh on its PATH. Tests use it too.
const realGh = process.env.AGENTBOX_GH_REAL || '/opt/gh/bin/gh';

function fail(msg) {
  process.stderr.write('agentbox: ' + msg + '\n');
  process.exit(1);
}

// A host other than github.com is not ours to answer for: the token is a
// github.com installation token and must never be sent anywhere else, so such a
// target runs the real gh unminted.
const isOtherHost = (h) => !!h && h.toLowerCase() !== 'github.com';

// repoSpec parses what gh accepts for a repository: [HOST/]OWNER/REPO, a URL
// (https://github.com/o/r[.git][/]) or the scp form git@github.com:o/r[.git].
// A spec naming another host is { other: true }. A value that is present but
// cannot be read is { bad: s }: the caller fails closed on it, because falling
// back to another source could mint for the wrong owner (a PAT owner's token
// is not scoped by GitHub, so the repo named here is the only scope).
function repoSpec(s) {
  if (!s) return null;
  const bad = { bad: s };
  let m = /^(?:https?|ssh|git):\/\/(?:[^@/]+@)?([^/:]+)(?::\d+)?(?:\/(.*))?$/i.exec(s);
  if (!m) m = /^(?:[^@/:]+@)?([^/:@]+):(.*)$/.exec(s);
  if (m) {
    if (isOtherHost(m[1])) return { other: true };
    const p = (m[2] || '').replace(/\/$/, '').replace(/\.git$/i, '').split('/');
    return p.length === 2 && p[0] && p[1] ? { owner: p[0], repo: p[1] } : bad;
  }
  const p = s.split('/');
  if (p.length === 3 && isOtherHost(p[0])) return { other: true };
  if (p.length === 3) p.shift();
  if (p.length !== 2 || !p[0] || !p[1]) return bad;
  return { owner: p[0], repo: p[1] };
}

// repoFromArgv returns the first -R/--repo target (or { other: true } when any
// flag points at another host, --hostname included).
function repoFromArgv(argv) {
  let found = null;
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--') break;
    let v = null;
    let host = null;
    if (a === '-R' || a === '--repo') v = argv[i + 1];
    else if (a.startsWith('--repo=')) v = a.slice('--repo='.length);
    else if (a.startsWith('-R') && !a.startsWith('--')) v = a.slice(2);
    else if (a === '--hostname') host = argv[i + 1];
    else if (a.startsWith('--hostname=')) host = a.slice('--hostname='.length);
    if (isOtherHost(host)) return { other: true };
    const r = repoSpec(v);
    if (r && (r.other || r.bad)) return r;
    if (r && !found) found = r;
  }
  return found;
}

const remoteRes = [
  /^(?:https?|ssh|git):\/\/(?:[^@/]+@)?github\.com(?::\d+)?\/([^/]+)\/([^/]+?)(?:\.git)?\/?$/i, // URL forms
  /^(?:[^@/]+@)?github\.com:([^/]+)\/([^/]+?)(?:\.git)?\/?$/i, // scp-like: git@github.com:o/r.git
];

function repoFromOrigin() {
  const r = spawnSync('git', ['remote', 'get-url', 'origin'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], timeout: 5000 });
  if (r.status !== 0 || !r.stdout) return null;
  const url = r.stdout.trim();
  for (const re of remoteRes) {
    const m = re.exec(url);
    if (m) return { owner: m[1], repo: m[2] };
  }
  return null;
}

function resolve(argv) {
  if (isOtherHost(process.env.GH_HOST)) return { other: true };
  const r = repoFromArgv(argv) || repoSpec(process.env.GH_REPO) || repoFromOrigin();
  if (r) return r; // may be { bad } or { other }
  const first = (process.env.AGENTBOX_GH_OWNERS || '').split(',').map((s) => s.trim()).filter(Boolean)[0];
  return first ? { owner: first } : null;
}

function mint(payload, cb) {
  const body = JSON.stringify(payload);
  const opts = {
    method: 'POST', path: '/v1/github/token', timeout: 15000,
    headers: { Authorization: 'Bearer ' + tok, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) },
  };
  if (ep.startsWith('unix://')) {
    opts.socketPath = ep.slice('unix://'.length);
  } else {
    let u;
    try { u = new URL(ep); } catch (e) { return fail('bad AGENTBOX_BROKER ' + JSON.stringify(ep)); }
    if (u.protocol !== 'http:') return fail('AGENTBOX_BROKER must be unix:// or http://');
    opts.host = u.hostname;
    opts.port = u.port;
  }
  const req = http.request(opts, (res) => {
    let out = '';
    res.setEncoding('utf8');
    res.on('data', (c) => {
      out += c;
      if (out.length > 65536) { req.destroy(); fail('broker reply too large'); }
    });
    res.on('end', () => {
      if (res.statusCode === 200) {
        let t = '';
        try { t = JSON.parse(out).token || ''; } catch (e) { /* handled below */ }
        return t ? cb(t) : fail('broker reply had no token');
      }
      let msg = out.trim();
      try { msg = JSON.parse(out).error || msg; } catch (e) { /* not JSON */ }
      fail(msg || 'broker returned HTTP ' + res.statusCode);
    });
  });
  req.on('timeout', () => req.destroy(new Error('timed out after 15s')));
  req.on('error', (e) => fail('CuliBox broker unreachable (' + e.message + ')'));
  req.end(body);
}

function runGh(args, env) {
  // Handlers go in before spawn: the child can run (and be signalled) before
  // spawn returns, and a signal with no handler kills us instead of reaching gh.
  let child;
  for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => child && child.kill(sig));
  child = spawn(realGh, args, { env, stdio: 'inherit' });
  child.on('error', (e) => {
    process.stderr.write('agentbox: cannot run gh (' + e.message + ')\n');
    process.exit(127);
  });
  child.on('close', (code, signal) => {
    process.exit(signal ? 128 + (os.constants.signals[signal] || 0) : code);
  });
}

function main() {
  if (!ep || !tok) fail('not a CuliBox GitHub session (AGENTBOX_BROKER / AGENTBOX_BROKER_TOKEN unset)');
  const args = process.argv.slice(2);
  const r = resolve(args);
  if (r && r.bad) fail('cannot read the repository in ' + JSON.stringify(r.bad) + '; use -R OWNER/REPO');
  if (!r) fail('cannot tell which GitHub owner to use (no -R, GH_REPO, origin remote or AGENTBOX_GH_OWNERS)');
  // Another host: no broker call, no token. The box holds no credential for it.
  if (r.other) return runGh(args, { ...process.env });
  mint(r.repo ? { owner: r.owner, repo: r.repo } : { owner: r.owner }, (token) => {
    runGh(args, { ...process.env, GH_TOKEN: token });
  });
}

main();
