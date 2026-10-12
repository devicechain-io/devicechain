// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The publisher, run end to end against a fake npm and a fake OIDC exchange.
//
//   node --test scripts/publish-packages.test.mjs
//
// 🔴 WHAT THIS PINS: a release that cannot publish EVERY package refuses before it
// publishes ANY. An npm version is immutable, so "brand, client and dashboards went
// out, then widget-sdk failed" is not a retryable failure — those numbers are spent.
// The two states that make a package unpublishable from the release job are both
// checked up front: the package does not exist on the registry yet (its first
// version has to be a manual publish), and it exists but has no trusted publisher
// configured for release.yml (the OIDC exchange is refused, and `npm publish` would
// then fail with a bare 404 halfway down the chain).
//
// Black box on purpose. The script under test is a COPY of scripts/, run as a child
// process with `npm_execpath` pointing at a fake npm-cli.js (which records every
// publish) and with `fetch` replaced by a preloaded module (which plays the GitHub
// token endpoint and npm's OIDC exchange). Nothing in the script is stubbed, so what
// is tested is the script release.yml runs.

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { after, describe, it } from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { ORDER } from './packages.mjs';
import { stampManifest } from './set-package-versions.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const realPackages = path.join(path.dirname(here), 'packages');
const scratch = mkdtempSync(path.join(tmpdir(), 'dc-publish-test-'));
after(() => rmSync(scratch, { recursive: true, force: true }));

const VERSION = '0.20.0-rc.1';

// npm, as far as the publisher can tell. CommonJS and named npm-cli.js, because that is
// what launch.mjs accepts from npm_execpath.
const FAKE_NPM = `
const fs = require('node:fs');
const path = require('node:path');
const state = JSON.parse(process.env.FAKE_NPM_STATE);
const log = process.env.FAKE_NPM_LOG;
const published = fs.existsSync(log) ? fs.readFileSync(log, 'utf8').split('\\n').filter(Boolean) : [];
const [cmd, ...rest] = process.argv.slice(2);
if (cmd === '--version') { console.log('11.6.0'); process.exit(0); }
if (cmd === 'config') { console.log('registry = "https://registry.npmjs.org/"'); process.exit(0); }
if (cmd === 'view') {
  const [name, field] = rest;
  if (state.absent.includes(name)) { console.error('npm error code E404'); process.exit(1); }
  const out = published.includes(name);
  if (field === 'versions') console.log(JSON.stringify(out ? ['0.19.0', state.version] : '0.19.0'));
  else console.log(JSON.stringify(out ? { latest: '0.19.0', next: state.version } : { latest: '0.19.0' }));
  process.exit(0);
}
if (cmd === 'publish') {
  const name = JSON.parse(fs.readFileSync(path.join(process.cwd(), 'package.json'), 'utf8')).name;
  if (state.untrusted.includes(name)) { console.error('npm error code E404\\nnpm error 404 Not Found - PUT'); process.exit(1); }
  fs.appendFileSync(log, name + '\\n');
  process.exit(0);
}
console.error('fake npm: unexpected ' + process.argv.slice(2).join(' '));
process.exit(2);
`;

// The GitHub OIDC token endpoint and npm's token exchange. Every request is recorded so
// a test can tell an exchange that was refused from one that was never attempted.
//
// The token endpoint only answers the request token GitHub hands the job, and issues a
// distinct ID token per request; the exchange only answers an ID token that endpoint
// issued. So an exchange sent with the wrong credential is refused here as it would be
// by npm, and the log shows which ID token each exchange carried.
const FAKE_FETCH = `
import fs from 'node:fs';
const state = JSON.parse(process.env.FAKE_NPM_STATE);
const EXCHANGE = 'https://registry.npmjs.org/-/npm/v1/oidc/token/exchange/package/';
let issued = 0;
globalThis.fetch = async (input, init = {}) => {
  const url = String(input);
  const auth = (init.headers ?? {}).Authorization ?? '';
  fs.appendFileSync(process.env.FAKE_FETCH_LOG, (init.method ?? 'GET') + ' ' + url + ' ' + auth + '\\n');
  if (url.startsWith('https://token.actions.test/')) {
    if (auth !== 'Bearer request-token') return new Response('bad request token', { status: 401 });
    if (state.githubRefuses) return Response.json({ message: 'no id-token permission' }, { status: 403 });
    const audience = new URL(url).searchParams.get('audience');
    if (audience !== 'npm:registry.npmjs.org') return new Response('bad audience', { status: 400 });
    issued += 1;
    return Response.json({ value: 'github-id-token-' + issued });
  }
  if (url.startsWith(EXCHANGE) && init.method === 'POST') {
    const m = /^Bearer github-id-token-(\\d+)$/.exec(auth);
    if (!m || Number(m[1]) > issued) return Response.json({ message: 'invalid id token' }, { status: 401 });
    if (state.exchange === 'down') return new Response('upstream unavailable', { status: 503 });
    if (state.exchange === 'unreachable') throw new TypeError('fetch failed');
    const name = decodeURIComponent(url.slice(EXCHANGE.length).split('?')[0]);
    if (state.untrusted.includes(name) || state.absent.includes(name)) {
      return Response.json({ message: 'No trusted publisher configured for this package' }, { status: 404 });
    }
    if (state.exchange === 'tokenless') return Response.json({}, { status: 201 });
    return Response.json({ token: 'npm-publish-token-secret' }, { status: 201 });
  }
  return new Response('unexpected', { status: 500 });
};
`;

let runs = 0;

// A fresh frontend/ copy (scripts + STAMPED manifests) per run, so one run's publish
// log cannot leak into the next.
function runPublisher({ absent = [], untrusted = [], githubRefuses = false, exchange = 'ok' } = {}) {
  runs += 1;
  const root = path.join(scratch, `run-${runs}`);
  const scripts = path.join(root, 'frontend', 'scripts');
  mkdirSync(scripts, { recursive: true });
  for (const f of readdirSync(here).filter((f) => f.endsWith('.mjs') && !f.endsWith('.test.mjs'))) {
    cpSync(path.join(here, f), path.join(scripts, f));
  }
  for (const name of ORDER) {
    const dir = path.join(root, 'frontend', 'packages', name);
    mkdirSync(dir, { recursive: true });
    const pkg = JSON.parse(readFileSync(path.join(realPackages, name, 'package.json'), 'utf8'));
    stampManifest(pkg, VERSION);
    writeFileSync(path.join(dir, 'package.json'), `${JSON.stringify(pkg, null, 2)}\n`);
  }
  const fake = path.join(root, 'fake');
  mkdirSync(fake);
  writeFileSync(path.join(fake, 'npm-cli.js'), FAKE_NPM);
  writeFileSync(path.join(fake, 'fetch.mjs'), FAKE_FETCH);
  const npmLog = path.join(fake, 'published.log');
  const fetchLog = path.join(fake, 'fetch.log');

  const result = spawnSync(
    process.execPath,
    [
      '--import',
      pathToFileURL(path.join(fake, 'fetch.mjs')).href,
      path.join(scripts, 'publish-packages.mjs'),
      '--version',
      VERSION,
      '--tag',
      'next',
    ],
    {
      encoding: 'utf8',
      // A run that gets past the refusals reaches the registry assertion, which polls
      // for fifteen minutes; bounded so such a defect fails the test instead of hanging it.
      timeout: 120000,
      env: {
        ...process.env,
        npm_execpath: path.join(fake, 'npm-cli.js'),
        GITHUB_ACTIONS: 'true',
        ACTIONS_ID_TOKEN_REQUEST_URL: 'https://token.actions.test/token?api-version=2.0',
        ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'request-token',
        FAKE_NPM_STATE: JSON.stringify({ version: VERSION, absent, untrusted, githubRefuses, exchange }),
        FAKE_NPM_LOG: npmLog,
        FAKE_FETCH_LOG: fetchLog,
      },
    },
  );
  const lines = (file) => (existsSync(file) ? readFileSync(file, 'utf8').split('\n').filter(Boolean) : []);
  return {
    status: result.status,
    output: `${result.stdout}\n${result.stderr}`,
    published: lines(npmLog),
    fetched: lines(fetchLog),
  };
}

const ALL = ORDER.map((name) => `@devicechain/${name}`);
const exchanged = (run) =>
  run.fetched.filter((l) => l.startsWith('POST ')).map((l) => decodeURIComponent(l.split(' ')[1].split('/package/')[1]));
// Neither credential may reach the job log: the ID token, or the publish token npm mints.
function assertNoTokenPrinted(run) {
  assert.doesNotMatch(run.output, /github-id-token|npm-publish-token|request-token/, run.output);
}

describe('publish-packages refuses before publishing anything', () => {
  // The control. Without it, the refusals below could pass because the harness never
  // publishes at all.
  it('publishes every package, leaf-first, when every package is ready', () => {
    const run = runPublisher();
    assert.equal(run.status, 0, run.output);
    assert.deepEqual(run.published, ALL);
    // EVERY package was asked about, not just the one a refusal test happens to name.
    assert.deepEqual(exchanged(run), ALL, run.fetched.join('\n'));
    assertNoTokenPrinted(run);
  });

  it('asks the exchange as npm publish does: a fresh ID token for every package', () => {
    const run = runPublisher();
    assert.equal(run.status, 0, run.output);
    const used = run.fetched.filter((l) => l.startsWith('POST ')).map((l) => l.split(' ').slice(2).join(' '));
    assert.equal(used.length, ALL.length);
    assert.equal(new Set(used).size, used.length, `an ID token was replayed:\n${used.join('\n')}`);
  });

  it('a package with no trusted publisher stops the run before the first publish', () => {
    const run = runPublisher({ untrusted: ['@devicechain/widget-sdk'] });
    assert.notEqual(run.status, 0, run.output);
    assert.deepEqual(run.published, [], `published before refusing:\n${run.output}`);
    assert.match(run.output, /@devicechain\/widget-sdk/);
    assert.match(run.output, /trusted publisher/i);
    assert.match(run.output, /release\.yml/);
    // ...and it was the exchange that said so, not a guess.
    assert.ok(
      run.fetched.some((l) => l.startsWith('POST ') && l.includes('/package/@devicechain%2fwidget-sdk ')),
      run.fetched.join('\n'),
    );
    assertNoTokenPrinted(run);
  });

  it('an exchange answered without a token is a refusal, not a ready', () => {
    const run = runPublisher({ exchange: 'tokenless' });
    assert.notEqual(run.status, 0, run.output);
    assert.deepEqual(run.published, []);
    assert.match(run.output, /NOTHING WAS PUBLISHED/);
    assert.match(run.output, /no token/);
  });

  it('GitHub refusing the ID token publishes nothing', () => {
    const run = runPublisher({ githubRefuses: true });
    assert.notEqual(run.status, 0, run.output);
    assert.deepEqual(run.published, []);
    assert.match(run.output, /OIDC ID token/);
  });

  // A registry that cannot be asked is not a missing trusted publisher, and above all
  // not a "ready": it refuses everything, says npm could not be reached, and exits
  // cleanly rather than with a stack trace.
  for (const exchange of ['down', 'unreachable']) {
    it(`an exchange that cannot be asked (${exchange}) publishes nothing and says so`, () => {
      const run = runPublisher({ exchange });
      assert.equal(run.status, 1, run.output);
      assert.deepEqual(run.published, []);
      assert.match(run.output, /could not be reached/);
      assert.match(run.output, /NOTHING WAS PUBLISHED/);
      assert.doesNotMatch(run.output, /no trusted publisher matching/);
      assertNoTokenPrinted(run);
    });
  }

  it('names EVERY package that has never been published, and publishes none', () => {
    const run = runPublisher({ absent: ['@devicechain/widget-sdk', '@devicechain/widgets'] });
    assert.notEqual(run.status, 0, run.output);
    assert.deepEqual(run.published, []);
    assert.match(run.output, /@devicechain\/widget-sdk/);
    assert.match(run.output, /@devicechain\/widgets\b/);
    assert.match(run.output, /npm publish/);
  });
});
