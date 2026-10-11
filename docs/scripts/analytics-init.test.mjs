// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// static/analytics.js: refuses to start PostHog without the URL scrubber, and starts it
// with the scrubber and without feature flags when it is present. Also pins the search-page
// title strings the scrubber knows to the translations the site actually ships.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { createRequire } from 'node:module';

const source = readFileSync(new URL('../static/analytics.js', import.meta.url), 'utf8');
const scrub = createRequire(import.meta.url)('../static/analytics-scrub.js');

function run({ hostname = 'docs.devicechain.io', withScrubber = true } = {}) {
  const inits = [];
  const window = {
    location: { hostname },
    posthog: { init: (...args) => inits.push(args) },
  };
  if (withScrubber) window.dcAnalyticsScrub = scrub;
  vm.runInNewContext(source, { window });
  return inits;
}

test('without the scrubber PostHog is never initialised', () => {
  assert.equal(run({ withScrubber: false }).length, 0);
});

test('with the scrubber it initialises with before_send and without feature flags', () => {
  const inits = run();
  assert.equal(inits.length, 1);
  const config = inits[0][1];
  assert.equal(config.before_send, scrub.scrubEvent);
  assert.equal(config.advanced_disable_flags, true);
});

test('it still reports only from the published host', () => {
  assert.equal(run({ hostname: 'localhost' }).length, 0);
});

test('the scrubber title templates equal the shipped search-page titles', () => {
  const messages = ['es', 'zh-CN'].map((locale) => {
    const file = new URL(`../i18n/${locale}/code.json`, import.meta.url);
    return JSON.parse(readFileSync(file, 'utf8'))['theme.SearchPage.existingResultsTitle'].message;
  });
  const en = 'Search results for "{query}"'; // the plugin's default message
  for (const m of [en, ...messages]) {
    assert.ok(scrub.SEARCH_TITLE_TEMPLATES.includes(m), `missing template: ${m}`);
  }
});
