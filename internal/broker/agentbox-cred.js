#!/usr/bin/env node
// agent-box credential client. Runs inside a box and asks the agent-box broker
// on the host for short-lived credentials. Never prints the session token.
//
//   agentbox-cred.js              AWS credential_process: prints the credential JSON.
//   agentbox-cred.js aws <name>   the same, for one named AWS connection.
//   agentbox-cred.js aws          a session with several connections has no default:
//                                 prints guidance and exits 1 (no broker call).
//   agentbox-cred.js github-git   git credential helper (`get` only): prints a GitHub
//                                 installation token for the repo git is asking about.
'use strict';
const http = require('http');
const fs = require('fs');

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
  req.on('error', (e) => finish(new Error('CuliBox broker unreachable (' + e.message + ')')));
  if (payload !== undefined) req.write(payload);
  req.end();
}

function brokerMessage(body, status) {
  let msg = body.trim();
  try { msg = JSON.parse(body).error || msg; } catch (e) { /* not JSON */ }
  return msg || 'broker returned HTTP ' + status;
}

const CONN_NAME_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;
const NO_DEFAULT_GUIDANCE = 'several AWS connections are bound — pick one with --profile <name> or AWS_PROFILE; run agentbox-env --list';

// awsCredentialProcess serves credential_process. With no connection it sends
// today's request (no body); with one it names the connection in a JSON body.
function awsCredentialProcess(conn) {
  function fail(msg) {
    process.stderr.write('agentbox: ' + msg + '\n');
    process.exit(1);
  }
  if (conn !== undefined && !CONN_NAME_RE.test(conn)) fail('bad AWS connection name ' + JSON.stringify(conn));
  if (!ep || !tok) fail('not a CuliBox AWS session (AGENTBOX_BROKER / AGENTBOX_BROKER_TOKEN unset)');
  request('/v1/credentials', conn === undefined ? undefined : { name: conn }, 10000, (err, status, body) => {
    if (err) fail(err.message);
    if (status === 200) { process.stdout.write(body); return; }
    fail(brokerMessage(body, status));
  });
}

const HOST_PUSH_POINTER = 'to push from Host, start a session with "Use this machine\'s login" for GitHub.';

// hostPushHint says once per Host session that the GitHub binding cannot push.
// AGENTBOX_DIR is set only on Host; the marker file is the once-per-session memory
// (EEXIST = already shown, any other error still prints).
function hostPushHint(contents) {
  const dir = process.env.AGENTBOX_DIR || '';
  if (!dir) return;
  try {
    fs.writeFileSync(dir + '/push-hint-shown', '', { flag: 'wx', mode: 0o600 });
  } catch (e) {
    if (e && e.code === 'EEXIST') return;
  }
  process.stderr.write('agentbox: the GitHub binding can read but not push (contents: ' + contents + '); ' + HOST_PUSH_POINTER + '\n');
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
    if (!ep || !tok) return warn('not a CuliBox GitHub session (AGENTBOX_BROKER / AGENTBOX_BROKER_TOKEN unset)');
    request('/v1/github/token', { owner: seg[0], repo: seg[1] }, 15000, (err, status, body) => {
      if (err) return warn(err.message);
      if (status !== 200) {
        warn(brokerMessage(body, status));
        if (process.env.AGENTBOX_DIR) warn(HOST_PUSH_POINTER);
        return;
      }
      let t = '';
      let perms = null;
      try { const r = JSON.parse(body); t = r.token || ''; perms = r.permissions || null; } catch (e) { /* handled below */ }
      if (!t) return warn('broker reply had no token');
      process.stdout.write('username=x-access-token\npassword=' + t + '\n');
      if (perms && perms.contents !== 'write') hostPushHint(perms.contents || 'none');
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
else if (process.argv[2] === 'aws') {
  if (process.argv[3] === undefined) {
    // The [default] profile of a session with several connections: nothing to
    // mint, so no broker call (and no token needed to be told what to do).
    process.stderr.write('agentbox: ' + NO_DEFAULT_GUIDANCE + '\n');
    process.exit(1);
  }
  awsCredentialProcess(process.argv[3]);
} else awsCredentialProcess();
