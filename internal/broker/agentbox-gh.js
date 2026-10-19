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
// AGENTBOX_GH_REAL is a test-only override of the real gh's path.
const realGh = process.env.AGENTBOX_GH_REAL || '/opt/gh/bin/gh';

function fail(msg) {
  process.stderr.write('agentbox: ' + msg + '\n');
  process.exit(1);
}

// repoSpec parses [HOST/]OWNER/REPO as gh accepts it. Another host than
// github.com is not ours to answer for.
function repoSpec(s) {
  if (!s) return null;
  const p = s.split('/');
  if (p.length === 3 && p[0].toLowerCase() === 'github.com') p.shift();
  if (p.length !== 2 || !p[0] || !p[1]) return null;
  return { owner: p[0], repo: p[1] };
}

function repoFromArgv(argv) {
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--') return null;
    let v = null;
    if (a === '-R' || a === '--repo') v = argv[i + 1];
    else if (a.startsWith('--repo=')) v = a.slice('--repo='.length);
    else if (a.startsWith('-R') && !a.startsWith('--')) v = a.slice(2);
    const r = repoSpec(v);
    if (r) return r;
  }
  return null;
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
  const r = repoFromArgv(argv) || repoSpec(process.env.GH_REPO) || repoFromOrigin();
  if (r) return r;
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
  req.on('error', (e) => fail('agent-box broker unreachable (' + e.message + ')'));
  req.end(body);
}

function main() {
  if (!ep || !tok) fail('not an agent-box GitHub session (AGENTBOX_BROKER / AGENTBOX_BROKER_TOKEN unset)');
  const args = process.argv.slice(2);
  const r = resolve(args);
  if (!r) fail('cannot tell which GitHub owner to use (no -R, GH_REPO, origin remote or AGENTBOX_GH_OWNERS)');
  mint(r.repo ? { owner: r.owner, repo: r.repo } : { owner: r.owner }, (token) => {
    const child = spawn(realGh, args, { env: { ...process.env, GH_TOKEN: token }, stdio: 'inherit' });
    for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => child.kill(sig));
    child.on('error', (e) => {
      process.stderr.write('agentbox: cannot run gh (' + e.message + ')\n');
      process.exit(127);
    });
    child.on('close', (code, signal) => {
      process.exit(signal ? 128 + (os.constants.signals[signal] || 0) : code);
    });
  });
}

main();
