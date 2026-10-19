#!/usr/bin/env node
// agent-box credential client. Runs inside a box and asks the agent-box broker
// on the host for short-lived credentials. Never prints the session token.
//
//   agentbox-cred.js              AWS credential_process: prints the credential JSON.
//   agentbox-cred.js github-git   git credential helper (`get` only): prints a GitHub
//                                 installation token for the repo git is asking about.
'use strict';
const http = require('http');

const ep = process.env.AGENTBOX_BROKER || '';
const tok = process.env.AGENTBOX_BROKER_TOKEN || '';

// request POSTs to the broker and calls cb(err, statusCode, body). The broker
// is reached over its unix socket (unix://) or, on macOS, over http:. A reply is
// capped at 64 KiB.
function request(path, payload, timeoutMs, cb) {
  const opts = { method: 'POST', path, headers: { Authorization: 'Bearer ' + tok }, timeout: timeoutMs };
  if (payload !== undefined) {
    payload = JSON.stringify(payload);
    opts.headers['Content-Type'] = 'application/json';
    opts.headers['Content-Length'] = Buffer.byteLength(payload);
  }
  if (ep.startsWith('unix://')) {
    opts.socketPath = ep.slice('unix://'.length);
  } else {
    let u;
    try { u = new URL(ep); } catch (e) { return cb(new Error('bad AGENTBOX_BROKER ' + JSON.stringify(ep))); }
    if (u.protocol !== 'http:') return cb(new Error('AGENTBOX_BROKER must be unix:// or http://'));
    opts.host = u.hostname;
    opts.port = u.port;
  }
  let done = false;
  const finish = (...a) => { if (!done) { done = true; cb(...a); } };
  const req = http.request(opts, (res) => {
    let body = '';
    res.setEncoding('utf8');
    res.on('data', (c) => {
      body += c;
      if (body.length > 65536) { req.destroy(); finish(new Error('broker reply too large')); }
    });
    res.on('end', () => finish(null, res.statusCode, body));
  });
  req.on('timeout', () => req.destroy(new Error('timed out after ' + timeoutMs / 1000 + 's')));
  req.on('error', (e) => finish(new Error('agent-box broker unreachable (' + e.message + ')')));
  if (payload !== undefined) req.write(payload);
  req.end();
}

function brokerMessage(body, status) {
  let msg = body.trim();
  try { msg = JSON.parse(body).error || msg; } catch (e) { /* not JSON */ }
  return msg || 'broker returned HTTP ' + status;
}

function awsCredentialProcess() {
  function fail(msg) {
    process.stderr.write('agentbox: ' + msg + '\n');
    process.exit(1);
  }
  if (!ep || !tok) fail('not an agent-box AWS session (AGENTBOX_BROKER / AGENTBOX_BROKER_TOKEN unset)');
  request('/v1/credentials', undefined, 10000, (err, status, body) => {
    if (err) fail(err.message);
    if (status === 200) { process.stdout.write(body); return; }
    fail(brokerMessage(body, status));
  });
}

// githubGit answers git's credential protocol. Anything it does not serve
// (store, erase, another host, another protocol) is a silent no-op, and a broker
// refusal prints to stderr but still exits 0 with nothing on stdout, so git fails
// the way it does for any missing credential.
function githubGit(op) {
  if (op !== 'get') return;
  let buf = '';
  let handled = false;
  const handle = () => {
    if (handled) return;
    handled = true;
    process.stdin.destroy(); // git may hold its end open; we have what we need
    const attrs = {};
    for (const line of buf.split('\n')) {
      if (line === '') break;
      const i = line.indexOf('=');
      if (i > 0) attrs[line.slice(0, i)] = line.slice(i + 1);
    }
    if (attrs.protocol !== 'https' || attrs.host !== 'github.com') return;
    const seg = (attrs.path || '').replace(/\/+$/, '').replace(/\.git$/, '').replace(/\/+$/, '').split('/').filter(Boolean);
    if (seg.length < 2) return;
    const warn = (m) => process.stderr.write('agentbox: ' + m + '\n');
    if (!ep || !tok) return warn('not an agent-box GitHub session (AGENTBOX_BROKER / AGENTBOX_BROKER_TOKEN unset)');
    request('/v1/github/token', { owner: seg[0], repo: seg[1] }, 15000, (err, status, body) => {
      if (err) return warn(err.message);
      if (status !== 200) return warn(brokerMessage(body, status));
      let t = '';
      try { t = JSON.parse(body).token || ''; } catch (e) { /* handled below */ }
      if (!t) return warn('broker reply had no token');
      process.stdout.write('username=x-access-token\npassword=' + t + '\n');
    });
  };
  process.stdin.setEncoding('utf8');
  process.stdin.on('data', (c) => {
    buf += c;
    if (buf.length > 65536) { handled = true; process.stdin.destroy(); return; }
    if (buf.includes('\n\n') || buf.startsWith('\n')) handle();
  });
  process.stdin.on('end', handle);
}

if (process.argv[2] === 'github-git') githubGit(process.argv[3]);
else awsCredentialProcess();
