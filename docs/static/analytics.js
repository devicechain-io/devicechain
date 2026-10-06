// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Starts PostHog product analytics for the docs. Loaded (via `scripts` in
// docusaurus.config.ts) after /posthog/posthog-<ver>.js, which leaves an
// un-initialised client on window.posthog. The marketing site at devicechain.io runs the
// same config from its own copy, into the same PostHog project.
//
// The client is a pinned, same-origin file rather than PostHog's loader snippet (or the
// posthog-docusaurus plugin, which injects that snippet), because the snippet fetches
// whatever array.js PostHog serves that day: an unpinned third-party script on every page.
//
// /ingest is proxied to PostHog's US region by static/_redirects, so every request —
// events, remote config, lazily loaded extensions — is same-origin. That keeps analytics
// working past ad-blockers that filter the posthog.com hostnames.
//
// COOKIELESS: nothing is written to cookies or localStorage, so no consent banner is
// needed. PostHog identifies a visitor server-side from a daily-rotating hash, which also
// joins a visit across devicechain.io and docs.devicechain.io. This requires "Cookieless
// server hash mode" to be enabled in the PostHog project settings — without it, events are
// dropped.
//
// capture_pageview is 'history_change' because Docusaurus is a single-page app: after the
// first load, navigating between pages changes the URL without a page load, and the default
// would record only the page someone landed on.
(() => {
  'use strict';

  const posthog = window.posthog;
  if (!posthog || typeof posthog.init !== 'function') return;
  // Only the published site reports; a local `npm start` or a deploy preview would
  // otherwise mix test traffic into the real numbers.
  if (window.location.hostname !== 'docs.devicechain.io') return;

  posthog.init('phc_kmuCyiJ9i3VSgPNS8gEtV4AL5FAzHJLe7iSG465fK2S3', {
    api_host: '/ingest',
    ui_host: 'https://us.posthog.com',
    cookieless_mode: 'always',
    person_profiles: 'identified_only',
    capture_pageview: 'history_change',
  });
})();
