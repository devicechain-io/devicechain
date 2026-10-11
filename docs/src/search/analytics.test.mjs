// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Run with: node --test src/search/analytics.test.mjs   (npm run test:search)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  MAX_QUERY_LENGTH,
  createSearchTracker,
  sanitizeQuery,
} from './analytics.mjs';

// ---------------------------------------------------------------- sanitizer

test('a normal query is trimmed, collapsed and lowercased', () => {
  assert.deepEqual(sanitizeQuery('  Device   Profile  '), { redacted: false, query: 'device profile' });
});

test('blank queries report nothing', () => {
  assert.equal(sanitizeQuery(''), null);
  assert.equal(sanitizeQuery('   \t '), null);
  assert.equal(sanitizeQuery(undefined), null);
});

test('ordinary docs vocabulary passes through, including look-alikes', () => {
  for (const q of [
    'mqtt',
    'bearer token',
    'bearer authentication',
    'how do i configure alarm rules',
    'device-profile-versioning-and-rollback-rules', // long hyphenated slug, no digits
    'DeviceProfileVersionRecordDefinition', // long CamelCase identifier, no digits
    'reference/graphql/device-management',
    'https://docs.devicechain.io/quickstart',
    '设备 配置',
    'configuración de dispositivos',
    'v1.0.0 release',
    'tls 1.3',
  ]) {
    const got = sanitizeQuery(q);
    assert.equal(got.redacted, false, `${q} must not be redacted`);
    assert.equal(got.query, q.toLowerCase().replace(/\s+/g, ' '));
  }
});

test('sensitive queries are dropped entirely, not truncated or masked', () => {
  const sensitive = [
    'derek.adams@sitewhere.com',
    'why does user@example.com fail',
    'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U',
    'Bearer abcDEF1234567890abcdef',
    'bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc',
    '0123456789abcdef0123456789abcdef', // 32 hex
    'AAAAAAAAAAAAAAAAAAAAAAAA', // 24 hex-only (A is hex)
    'dGhpcyBpcyBhIHNlY3JldCBrZXkgMTIz', // base64
    'Zm9vYmFyMTIzNDU2Nzg5MEFCQ0RFRg==',
    'key_AbCdEf0123456789GhIjKlMnOpQr',
    'https://user:hunter2@host.example.com/path',
    'postgres://admin@db.internal:5432/x',
    'https://api.example.com/v1?token=abc123',
    'curl "https://x.io/a?api_key=zzz"',
  ];
  for (const q of sensitive) {
    assert.deepEqual(sanitizeQuery(q), { redacted: true }, q);
  }
});

test('a secret is caught even when it is not the first word and after a long prefix', () => {
  const q = `${'word '.repeat(30)}0123456789abcdef0123456789abcdef`;
  assert.deepEqual(sanitizeQuery(q), { redacted: true });
});

test('long queries are capped at the limit', () => {
  const got = sanitizeQuery('a '.repeat(200));
  assert.equal(got.redacted, false);
  assert.equal(got.query.length, MAX_QUERY_LENGTH);
  assert.equal(MAX_QUERY_LENGTH, 100);
});

// ------------------------------------------------------------------ tracker

/** A manual clock standing in for setTimeout/clearTimeout. */
function fakeClock() {
  let now = 0;
  let nextId = 1;
  const timers = new Map();
  return {
    setTimer(fn, ms) {
      const id = nextId++;
      timers.set(id, { at: now + ms, fn });
      return id;
    },
    clearTimer(id) {
      timers.delete(id);
    },
    tick(ms) {
      const target = now + ms;
      for (;;) {
        const due = [...timers.entries()]
          .filter(([, t]) => t.at <= target)
          .sort((a, b) => a[1].at - b[1].at)[0];
        if (!due) break;
        timers.delete(due[0]);
        now = due[1].at;
        due[1].fn();
      }
      now = target;
    },
    pending: () => timers.size,
  };
}

function fakePostHog(overrides = {}) {
  const events = [];
  return {
    events,
    __loaded: true,
    has_opted_out_capturing: () => false,
    capture: (name, props) => events.push({ name, props }),
    ...overrides,
  };
}

function harness({ posthog = fakePostHog(), count = 3, path = '/guides/a', locale = 'en' } = {}) {
  const clock = fakeClock();
  const state = { posthog, count, path, locale };
  const tracker = createSearchTracker({
    getPostHog: () => state.posthog,
    getLocale: () => state.locale,
    getPagePath: () => state.path,
    getResultCount: () => state.count,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  return { tracker, clock, state };
}

test('typing sends nothing until 1s of quiet, then exactly one docs_search', () => {
  const { tracker, clock, state } = harness();
  for (const v of ['a', 'al', 'ala', 'alar', 'alarm']) {
    tracker.input(v);
    clock.tick(300); // never 1s between keystrokes
  }
  assert.equal(state.posthog.events.length, 0, 'no event per keystroke');
  clock.tick(700);
  assert.deepEqual(state.posthog.events, [
    {
      name: 'docs_search',
      props: { query: 'alarm', redacted: false, locale: 'en', result_count: 3, page_path: '/guides/a' },
    },
  ]);
});

test('zero results are sent as result_count 0', () => {
  const { tracker, clock, state } = harness({ count: 0 });
  tracker.input('nonexistent thing');
  clock.tick(1000);
  assert.equal(state.posthog.events.length, 1);
  assert.equal(state.posthog.events[0].props.result_count, 0);
});

test('the same query is reported once per page view, a different one again', () => {
  const { tracker, clock, state } = harness();
  tracker.input('mqtt');
  clock.tick(1000);
  tracker.input('mqtt ');
  clock.tick(1000);
  tracker.input('MQTT');
  clock.tick(1000);
  assert.equal(state.posthog.events.length, 1);
  tracker.input('kafka');
  clock.tick(1000);
  assert.equal(state.posthog.events.length, 2);
  // A new page view starts over.
  state.path = '/guides/b';
  tracker.pageChanged();
  tracker.input('mqtt');
  clock.tick(1000);
  assert.equal(state.posthog.events.length, 3);
  assert.equal(state.posthog.events[2].props.page_path, '/guides/b');
});

test('a sensitive query is reported with redacted:true and no query', () => {
  const { tracker, clock, state } = harness();
  tracker.input('me@example.com');
  clock.tick(1000);
  assert.deepEqual(state.posthog.events[0].props, {
    redacted: true,
    locale: 'en',
    result_count: 3,
    page_path: '/guides/a',
  });
  assert.ok(!('query' in state.posthog.events[0].props));
});

test('results not on screen yet: waits and retries a bounded number of times', () => {
  const { tracker, clock, state } = harness({ count: null });
  tracker.input('alarms');
  clock.tick(1000);
  assert.equal(state.posthog.events.length, 0, 'unknown is not zero');
  state.count = 5; // the index finished loading
  clock.tick(1000);
  assert.equal(state.posthog.events.length, 1);
  assert.equal(state.posthog.events[0].props.result_count, 5);

  // never resolves: gives up instead of polling forever
  const h = harness({ count: null });
  h.tracker.input('x');
  h.clock.tick(60_000);
  assert.equal(h.state.posthog.events.length, 0);
  assert.equal(h.clock.pending(), 0);
});

test('clearing the input cancels the pending report', () => {
  const { tracker, clock, state } = harness();
  tracker.input('alarm');
  clock.tick(500);
  tracker.input('');
  clock.tick(5000);
  assert.equal(state.posthog.events.length, 0);
});

test('selecting a result reports the search at once, then the click with the landed URL and rank', () => {
  const { tracker, clock, state } = harness();
  tracker.input('alarm');
  clock.tick(200); // well inside the debounce
  tracker.select(2);
  assert.equal(state.posthog.events.length, 1);
  assert.equal(state.posthog.events[0].name, 'docs_search');
  tracker.navigated('/concepts/alarms#levels');
  assert.equal(state.posthog.events.length, 2);
  assert.deepEqual(state.posthog.events[1], {
    name: 'docs_search_result_clicked',
    props: {
      query: 'alarm',
      redacted: false,
      locale: 'en',
      clicked_url: '/concepts/alarms#levels',
      rank: 2,
      page_path: '/guides/a',
      result_count: 3,
    },
  });
  clock.tick(5000);
  assert.equal(state.posthog.events.length, 2, 'debounce does not report the query a second time');
});

test('a click after the search was already reported does not repeat docs_search', () => {
  const { tracker, clock, state } = harness();
  tracker.input('alarm');
  clock.tick(1000);
  tracker.select(1);
  tracker.navigated('/concepts/alarms');
  assert.deepEqual(
    state.posthog.events.map((e) => e.name),
    ['docs_search', 'docs_search_result_clicked'],
  );
});

test('a click on a redacted query carries redacted:true and no query', () => {
  const { tracker, state } = harness();
  tracker.input('0123456789abcdef0123456789abcdef');
  tracker.select(1);
  tracker.navigated('/x');
  for (const e of state.posthog.events) {
    assert.equal(e.props.redacted, true);
    assert.ok(!('query' in e.props));
  }
});

test('a selection that never navigates does not claim a later page change', () => {
  const { tracker, clock, state } = harness();
  tracker.input('alarm');
  tracker.select(1);
  clock.tick(4000);
  tracker.navigated('/somewhere/else');
  assert.deepEqual(
    state.posthog.events.map((e) => e.name),
    ['docs_search'],
  );
});

test('a navigation with no selection is not a click', () => {
  const { tracker, state } = harness();
  tracker.navigated('/x');
  assert.equal(state.posthog.events.length, 0);
});

// ------------------------------------------------- absent / not loaded / opted out

for (const [label, make] of [
  ['absent', () => undefined],
  ['null', () => null],
  ['an un-initialised stub', () => fakePostHog({ __loaded: false })],
  ['opted out', () => fakePostHog({ has_opted_out_capturing: () => true })],
  ['a client without capture', () => ({ __loaded: true })],
]) {
  test(`nothing fires when PostHog is ${label}`, () => {
    const posthog = make();
    const { tracker, clock } = harness({ posthog });
    tracker.input('alarm');
    clock.tick(5000);
    tracker.select(1);
    tracker.navigated('/x');
    if (posthog && posthog.events) assert.equal(posthog.events.length, 0);
    // and nothing throws, nothing is left scheduled
    clock.tick(60_000);
    assert.equal(clock.pending(), 0);
  });
}

test('opting out mid-session stops reporting from that moment', () => {
  const posthog = fakePostHog();
  const { tracker, clock } = harness({ posthog });
  tracker.input('first');
  clock.tick(1000);
  assert.equal(posthog.events.length, 1);
  posthog.has_opted_out_capturing = () => true;
  tracker.input('second');
  clock.tick(1000);
  tracker.select(1);
  tracker.navigated('/x');
  assert.equal(posthog.events.length, 1);
});

test('a client that appears later (analytics.js loads after the page) is picked up', () => {
  const { tracker, clock, state } = harness({ posthog: undefined });
  tracker.input('late');
  clock.tick(5000);
  state.posthog = fakePostHog();
  tracker.input('late again');
  clock.tick(1000);
  assert.equal(state.posthog.events.length, 1);
});
