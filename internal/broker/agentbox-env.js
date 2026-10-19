#!/usr/bin/env node
// agent-box env wrapper. Runs inside a box (/opt/agentbox/agentbox-env.js).
//
//   agentbox-env NAME [NAME...] -- CMD [ARGS...]   run CMD with the named tokens in its env
//   agentbox-env --list                            show what is bound (never a value)
//
// It asks the agent-box broker on the host for each named static token, then runs
// CMD (no shell) with the token(s) in its env for that one process only. Nothing
// is fetched until the wrapper runs, and a token is never in the session env,
// never on disk and never printed. If any fetch fails, or two entries would set
// the same variable, nothing is run.
//
// The transport below repeats the one in agentbox-gh.js on purpose: each script
// is a single file the box mounts, and a binding's directory holds only what it
// needs.
'use strict';
const http = require('http');
const fs = require('fs');
const { spawn } = require('child_process');
const os = require('os');

const ep = process.env.AGENTBOX_BROKER || '';
const tok = process.env.AGENTBOX_BROKER_TOKEN || '';
// Test-only overrides: the listing path, and the fetch timeout (default 10 s).
const listFile = process.env.AGENTBOX_ENV_LIST_FILE || '/opt/agentbox/tokens.json';
// The override can only shorten the default: a non-finite or junk value is ignored,
// a huge one is capped (Node turns an overflowing delay into a 1 ms timer).
const timeoutOverride = Number(process.env.AGENTBOX_ENV_TIMEOUT_MS);
const timeoutMs = Number.isFinite(timeoutOverride) && timeoutOverride > 0 ? Math.min(timeoutOverride, 10000) : 10000;

const USAGE = 'usage: agentbox-env NAME [NAME...] -- CMD [ARGS...]\n       agentbox-env --list\n';

function fail(msg, code) {
  process.stderr.write('agentbox-env: ' + msg + '\n');
  process.exit(code || 1);
}

function usage() {
  process.stderr.write(USAGE);
  process.exit(2);
}

// fetchToken asks the broker for one named entry. cb gets the parsed reply
// ({ value, env, config_env }); every failure exits with "<name>: <message>".
function fetchToken(name, cb) {
  const die = (m) => fail(name + ': ' + m);
  const body = JSON.stringify({ name });
  const opts = {
    method: 'POST', path: '/v1/static/token', timeout: timeoutMs,
    headers: { Authorization: 'Bearer ' + tok, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) },
  };
  if (ep.startsWith('unix://')) {
    opts.socketPath = ep.slice('unix://'.length);
  } else {
    let u;
    try { u = new URL(ep); } catch (e) { return die('bad AGENTBOX_BROKER ' + JSON.stringify(ep)); }
    if (u.protocol !== 'http:') return die('AGENTBOX_BROKER must be unix:// or http://');
    opts.host = u.hostname;
    opts.port = u.port;
  }
  const req = http.request(opts, (res) => {
    let out = '';
    res.setEncoding('utf8');
    res.on('data', (c) => {
      out += c;
      if (out.length > 65536) { req.destroy(); die('broker reply too large'); }
    });
    res.on('end', () => {
      if (res.statusCode === 200) {
        let r = null;
        try { r = JSON.parse(out); } catch (e) { /* handled below */ }
        if (!r || typeof r.value !== 'string' || r.value === '' || !Array.isArray(r.env) || r.env.length === 0) {
          return die('broker reply had no token');
        }
        return cb(r);
      }
      let msg = out.trim();
      try { msg = JSON.parse(out).error || msg; } catch (e) { /* not JSON */ }
      die(msg || 'broker returned HTTP ' + res.statusCode);
    });
  });
  req.on('timeout', () => req.destroy(new Error('timed out after ' + timeoutMs / 1000 + 's')));
  req.on('error', (e) => die('agent-box broker unreachable (' + e.message + ')'));
  req.end(body);
}

// fetchAll fetches the names one after another (a failure names the first one
// that fails) and calls cb with the replies in the order given.
function fetchAll(names, cb) {
  const got = [];
  const next = () => {
    if (got.length === names.length) return cb(got);
    fetchToken(names[got.length], (r) => { got.push(r); next(); });
  };
  next();
}

// combine builds the child's env additions, or fails when two entries set one name.
function combine(names, replies) {
  const owner = {};
  const add = {};
  replies.forEach((r, i) => {
    const cfg = r.config_env && typeof r.config_env === 'object' ? r.config_env : {};
    for (const k of [...r.env, ...Object.keys(cfg)]) {
      if (Object.prototype.hasOwnProperty.call(owner, k)) fail(owner[k] + ' and ' + names[i] + ' both set ' + k + '; name one');
      owner[k] = names[i];
    }
    Object.assign(add, cfg);
    for (const k of r.env) add[k] = r.value;
  });
  return add;
}

function run(cmd, args, env) {
  const child = spawn(cmd, args, { env, stdio: 'inherit' });
  for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) process.on(sig, () => child.kill(sig));
  child.on('error', (e) => fail('cannot run ' + cmd + ' (' + e.message + ')', 127));
  child.on('close', (code, signal) => {
    process.exit(signal ? 128 + (os.constants.signals[signal] || 0) : code);
  });
}

function list() {
  let entries;
  try {
    entries = JSON.parse(fs.readFileSync(listFile, 'utf8'));
    if (!Array.isArray(entries)) throw new Error('not a list');
  } catch (e) { return fail('cannot read the token list (' + e.message + ')'); }
  if (entries.length === 0) return process.stdout.write('no tokens are bound to this session\n');
  const blocks = entries.map((e) => {
    const head = [e.name];
    if (e.label) head.push('[' + e.label + ']');
    if (e.prod) head.push('[prod]');
    const lines = [head.join(' ')];
    if (e.provider) lines.push('  provider: ' + e.provider);
    if (e.description) lines.push('  ' + e.description);
    lines.push('  env: ' + (e.env || []).join(' '));
    const cfg = Object.entries(e.config_env || {}).map(([k, v]) => k + '=' + v);
    if (cfg.length) lines.push('  config: ' + cfg.join(' '));
    const hosts = (e.egress || []).map((h) => h.host + (h.port ? ':' + h.port : '') + (h.private ? ' (private)' : ''));
    if (hosts.length) lines.push('  hosts: ' + hosts.join(', '));
    if (e.mcp_server) lines.push('  mcp: ' + e.mcp_server);
    return lines.join('\n');
  });
  process.stdout.write(blocks.join('\n\n') + '\n');
}

function main() {
  const argv = process.argv.slice(2);
  if (argv.length === 1 && argv[0] === '--list') return list();
  const sep = argv.indexOf('--');
  if (sep < 0) return usage();
  const names = argv.slice(0, sep);
  const [cmd, ...args] = argv.slice(sep + 1);
  if (names.length === 0 || !cmd || new Set(names).size !== names.length) return usage();
  if (!ep || !tok) fail('not an agent-box session with tokens (AGENTBOX_BROKER / AGENTBOX_BROKER_TOKEN unset)');
  fetchAll(names, (replies) => {
    const add = combine(names, replies);
    run(cmd, args, { ...process.env, ...add });
  });
}

main();
