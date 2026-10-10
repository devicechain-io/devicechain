// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Second sanitizer / tracker pass: credential shapes the first set missed, the
// false-positive guards that keep ordinary queries searchable, and opt-out between a
// selection and the page it lands on.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createSearchTracker, sanitizeQuery } from './analytics.mjs';

// Vendor-prefixed fixtures are assembled at run time so no credential-shaped literal sits
// in the repository: secret scanners (GitHub push protection, Netlify's build scan) flag
// the literal and fail the push or the deploy, whatever the file is.
const tok = (prefix, body) => [prefix, body].join('');

test('more credential shapes are redacted', () => {
  const sensitive = [
    '123e4567-e89b-12d3-a456-426614174000', // UUID
    'device 123E4567-E89B-12D3-A456-426614174000 not found',
    tok('AKIA', 'IOSFODNN7EXAMPLE'), // AWS access key id
    tok('ASIA', 'Y34FZKBOKMUTVV7A'),
    'Basic dXNlcjpwYXNzd29yZDEyMw==',
    'basic YWRtaW46c2VjcmV0MTIz',
    'Bearer abc123def456ghi789',
    tok('xoxb', '-1234567890-abcdefghij'),
    tok('ghp', '_a1b2c3d4e5f6a7b8c9d0e1f2'),
    tok('sk', '-proj-abc123def456ghi789jkl'),
    tok('glpat', '-abc123def456ghi789'),
    'a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6', // 32 lowercase alphanumerics
    'https://x.io/cb#access_token=abc',
    '#access_token=abc123',
    'https://x.io/cb?client_secret=hunter2',
    '?refresh_token=abc',
    '&password=hunter2',
  ];
  for (const q of sensitive) assert.deepEqual(sanitizeQuery(q), { redacted: true }, q);
});

test('ordinary long lowercase queries with digits are NOT redacted', () => {
  for (const q of [
    'device-profile-v2-versioning-and-rollback-rules-2026',
    'how to upgrade from v0-18-0 to v0-19-0 without downtime',
    'rate limit 1000 events per second per tenant',
    'mqtt-broker-v5-shared-subscriptions-and-qos-2-delivery',
    'npm-packages-v2-reference-for-sdk-1-2-3',
    'basic authentication',
    'bearer token',
    'ask-ai-for-tls-1-3-support-in-v2',
    'section-heading-anchor-with-numbers-1234567890-abc',
    '#configuration',
  ]) {
    assert.equal(sanitizeQuery(q).redacted, false, q);
  }
});

function harness(posthog) {
  let now = 0;
  const timers = new Map();
  let id = 1;
  const state = { posthog };
  const tracker = createSearchTracker({
    getPostHog: () => state.posthog,
    getLocale: () => 'en',
    getPagePath: () => '/',
    getResultCount: () => 3,
    setTimer: (fn, ms) => {
      timers.set(id, { at: now + ms, fn });
      return id++;
    },
    clearTimer: (h) => timers.delete(h),
  });
  return { tracker, state };
}

test('opting out between selection and landing suppresses the click event', () => {
  const events = [];
  let optedOut = false;
  const posthog = {
    __loaded: true,
    has_opted_out_capturing: () => optedOut,
    capture: (name, props) => events.push({ name, props }),
  };
  const { tracker } = harness(posthog);
  tracker.input('alarm');
  tracker.select(1);
  assert.deepEqual(
    events.map((e) => e.name),
    ['docs_search'],
  );
  optedOut = true;
  tracker.navigated('/concepts/alarms');
  assert.deepEqual(
    events.map((e) => e.name),
    ['docs_search'],
    'no click once opted out',
  );
});

test('the click survives a tracker shared across a remount (same instance, new page)', () => {
  const events = [];
  const posthog = { __loaded: true, capture: (n, p) => events.push({ n, p }) };
  const { tracker } = harness(posthog);
  tracker.input('alarm');
  tracker.select(2);
  // remount: the same tracker is told about the landing page afterwards
  tracker.navigated('/reference/graphql/x');
  assert.equal(events.at(-1).n, 'docs_search_result_clicked');
  assert.equal(events.at(-1).p.rank, 2);
});

// ---- consent: the shape the live site has (cookieless_mode 'always')

function captureHarness(posthog) {
  const { tracker } = harness(posthog);
  return tracker;
}

test('cookieless consent "pending" (opted-out=true, is_capturing=true) still sends', () => {
  const events = [];
  const posthog = {
    __loaded: true,
    has_opted_out_capturing: () => true, // what cookieless_mode 'always' reports by default
    is_capturing: () => true,
    get_explicit_consent_status: () => 'pending',
    capture: (name) => events.push(name),
  };
  const tracker = captureHarness(posthog);
  tracker.input('alarm');
  tracker.select(1);
  tracker.navigated('/x');
  assert.deepEqual(events, ['docs_search', 'docs_search_result_clicked']);
});

test('explicit denial or is_capturing=false sends nothing', () => {
  for (const patch of [
    { get_explicit_consent_status: () => 'denied' },
    { is_capturing: () => false },
  ]) {
    const events = [];
    const posthog = {
      __loaded: true,
      has_opted_out_capturing: () => true,
      is_capturing: () => true,
      capture: (name) => events.push(name),
      ...patch,
    };
    const tracker = captureHarness(posthog);
    tracker.input('alarm');
    tracker.select(1);
    tracker.navigated('/x');
    assert.deepEqual(events, []);
  }
});

test('consent withdrawn between selection and landing suppresses the click', () => {
  const events = [];
  let capturing = true;
  const posthog = {
    __loaded: true,
    is_capturing: () => capturing,
    capture: (name) => events.push(name),
  };
  const tracker = captureHarness(posthog);
  tracker.input('alarm');
  tracker.select(1);
  capturing = false;
  tracker.navigated('/x');
  assert.deepEqual(events, ['docs_search']);
});

test('bearer boundary: 11 value characters pass, 12 are redacted', () => {
  assert.equal(sanitizeQuery('bearer abcdefghij1').redacted, false); // 11
  assert.deepEqual(sanitizeQuery('bearer abcdefghij12'), { redacted: true }); // 12
});
