// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// static/analytics-scrub.js is PostHog's before_send: nothing carrying a search query or a
// credential in a URL may leave the browser.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const { scrubUrl, scrubEvent } = createRequire(import.meta.url)('../static/analytics-scrub.js');

test('search and secret parameters are stripped from URLs', () => {
  assert.equal(scrubUrl('https://docs.devicechain.io/search/?q=my+secret+query'), 'https://docs.devicechain.io/search/');
  assert.equal(scrubUrl('/concepts/alarms?_highlight=alarm&_highlight=level#levels'), '/concepts/alarms#levels');
  assert.equal(scrubUrl('/x?a=1&q=foo&b=2'), '/x?a=1&b=2');
  assert.equal(scrubUrl('/x?Q=foo'), '/x');
  assert.equal(scrubUrl('/x?%71=foo'), '/x', 'percent-encoded name');
  assert.equal(scrubUrl('/cb?token=a&api_key=b&client_secret=c&access_token=d&x=1'), '/cb?x=1');
  assert.equal(scrubUrl('/cb#access_token=abc&state=1'), '/cb#state=1');
  assert.equal(scrubUrl('/cb#access_token=abc'), '/cb');
});

test('ordinary URLs and anchors are untouched', () => {
  for (const u of [
    'https://docs.devicechain.io/',
    '/concepts/architecture',
    '/concepts/architecture#data-lifecycle',
    '/x?page=2&lang=es',
    '/x?quarter=3',
    '',
  ]) {
    assert.equal(scrubUrl(u), u);
  }
  assert.equal(scrubUrl(undefined), undefined);
  assert.equal(scrubUrl(5), 5);
});

test('scrubEvent cleans pageview, referrer and session properties', () => {
  const ev = scrubEvent({
    event: '$pageview',
    properties: {
      $current_url: 'https://docs.devicechain.io/search/?q=hunter2',
      $pathname: '/search/',
      $referrer: 'https://docs.devicechain.io/search/?q=hunter2',
      $initial_referrer: 'https://docs.devicechain.io/search/?q=hunter2',
      $prev_pageview_pathname: '/concepts/a?_highlight=hunter2',
      $session_entry_url: 'https://docs.devicechain.io/x?token=abc',
      title: 'Search results for "hunter2"',
      $set_once: { $initial_current_url: 'https://docs.devicechain.io/y?q=hunter2' },
    },
  });
  const flat = JSON.stringify(ev);
  assert.ok(!flat.includes('hunter2'), flat);
  assert.ok(!flat.includes('token=abc'));
  assert.equal(ev.properties.$current_url, 'https://docs.devicechain.io/search/');
  assert.equal(ev.properties.$prev_pageview_pathname, '/concepts/a');
});

test('the page title is dropped on the search results page only', () => {
  const on = scrubEvent({ properties: { $current_url: 'https://d/es/search/?q=x', title: 'Resultados de "x"' } });
  assert.ok(!('title' in on.properties));
  const off = scrubEvent({ properties: { $current_url: 'https://d/concepts/alarms', title: 'Alarms' } });
  assert.equal(off.properties.title, 'Alarms');
});

test('autocapture links are cleaned in every place they appear', () => {
  const ev = scrubEvent({
    event: '$autocapture',
    properties: {
      $current_url: 'https://d/concepts/a',
      attr__href: '/search/?q=hunter2',
      $elements: [
        { tag_name: 'a', attr__href: '/search/?q=hunter2', $el_text: 'See all results' },
        { tag_name: 'a', href: '/x?_highlight=hunter2' },
      ],
      $elements_chain: 'a.x:attr__href="/search/?q=hunter2"nth-child="1";div:href="/y?token=abc"',
    },
  });
  assert.ok(!JSON.stringify(ev).includes('hunter2'));
  assert.ok(!JSON.stringify(ev).includes('token=abc'));
  assert.equal(ev.properties.$elements[0].$el_text, 'See all results');
});

test('non-event input does not throw', () => {
  assert.equal(scrubEvent(null), null);
  assert.deepEqual(scrubEvent({}), {});
});
