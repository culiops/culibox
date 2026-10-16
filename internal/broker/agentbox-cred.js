#!/usr/bin/env node
// agent-box credential_process client. Runs inside a box; asks the agent-box
// broker on the host for short-lived AWS credentials and prints them in the
// credential_process format. Never prints the token.
'use strict';
const http = require('http');

const ep = process.env.AGENTBOX_BROKER || '';
const tok = process.env.AGENTBOX_BROKER_TOKEN || '';

function fail(msg) {
  process.stderr.write('agentbox: ' + msg + '\n');
  process.exit(1);
}

if (!ep || !tok) fail('not an agent-box AWS session (AGENTBOX_BROKER / AGENTBOX_BROKER_TOKEN unset)');

const opts = { method: 'POST', path: '/v1/credentials', headers: { Authorization: 'Bearer ' + tok }, timeout: 10000 };
if (ep.startsWith('unix://')) {
  opts.socketPath = ep.slice('unix://'.length);
} else {
  let u;
  try { u = new URL(ep); } catch (e) { fail('bad AGENTBOX_BROKER ' + JSON.stringify(ep)); }
  if (u.protocol !== 'http:') fail('AGENTBOX_BROKER must be unix:// or http://');
  opts.host = u.hostname;
  opts.port = u.port;
}

const req = http.request(opts, (res) => {
  let body = '';
  res.setEncoding('utf8');
  res.on('data', (c) => {
    body += c;
    if (body.length > 65536) { req.destroy(); fail('broker reply too large'); }
  });
  res.on('end', () => {
    if (res.statusCode === 200) { process.stdout.write(body); return; }
    let msg = body.trim();
    try { msg = JSON.parse(body).error || msg; } catch (e) { /* not JSON */ }
    fail(msg || 'broker returned HTTP ' + res.statusCode);
  });
});
req.on('timeout', () => req.destroy(new Error('timed out after 10s')));
req.on('error', (e) => fail('agent-box broker unreachable (' + e.message + ')'));
req.end();
