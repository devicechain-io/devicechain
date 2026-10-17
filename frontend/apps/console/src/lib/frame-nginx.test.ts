// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Clickjacking protection lives in nginx, which no browser test reads: a deleted header
// line, or a new add_header in a location (which REPLACES every inherited one), would
// leave the console frameable with every other test green. This pins the headers on each
// location that can serve the console's HTML, and pins the dashboard viewer as the one
// thing that stays frameable, because embedding it is a documented feature.

import { describe, expect, it } from 'vitest';
import NGINX from '../../../../deploy/nginx.conf?raw';

/** Every `location` block as [matcher, body]. The config nests no braces inside one. */
function locations(): Array<[string, string]> {
  return [...NGINX.matchAll(/^[ \t]*location[ \t]+([^{#\n]+?)[ \t]*\{([^}]*)\}/gm)].map((m) => [m[1].trim(), m[2]]);
}

function body(matcher: string): string {
  const hit = locations().find(([m]) => m === matcher);
  expect(hit, `no location ${matcher}`).toBeDefined();
  return hit![1];
}

const CSP = /add_header\s+Content-Security-Policy\s+"frame-ancestors 'self'"\s+always\s*;/;
const XFO = /add_header\s+X-Frame-Options\s+"SAMEORIGIN"\s+always\s*;/;
/** The dashboard viewer's locations: embedding it is a documented feature. */
const DASH = ['/dash/', '= /dash/index.html', '= /dash/sso/complete', '/dash/assets/'];

describe('the console cannot be framed by another site', () => {
  // `/` serves every route and the public /invitation page; /index.html is where `/` and
  // every SPA fallback land by internal redirect; the SSO page sets other headers, which
  // is exactly when the inherited ones would silently vanish.
  it.each(['/', '= /index.html', '= /login/sso/complete'])('%s', (matcher) => {
    const b = body(matcher);
    expect(b).toMatch(CSP);
    expect(b).toMatch(XFO);
  });

  // The list above names the locations there are today. A location added later (an exact
  // match for /invitation, a regex for *.html, a `^~ /login/` prefix) would serve the
  // console's index without the headers and pass it, so every location is held to the
  // headers unless it is named here as exempt — the dashboard viewer, and the immutable
  // asset tree, which never serves HTML (a miss there is a 404, never the index).
  it('every location outside the exempt set carries them', () => {
    const exempt = new Set([...DASH, '= /dash', '/_app/']);
    for (const m of exempt) body(m); // an exemption for a location that is gone is stale
    for (const [m, b] of locations()) {
      if (exempt.has(m)) continue;
      expect(b, `location ${m}`).toMatch(CSP);
      expect(b, `location ${m}`).toMatch(XFO);
    }
  });
});

describe('the dashboard viewer stays embeddable', () => {
  it.each(DASH)('%s', (matcher) => {
    const b = body(matcher);
    expect(b).not.toMatch(/frame-ancestors/i);
    expect(b).not.toMatch(/X-Frame-Options/i);
  });

  it('is not given the headers at the server level either', () => {
    // Anything outside a location block would be inherited by /dash/.
    const outside = NGINX.replace(/^[ \t]*location[ \t]+[^{#\n]+\{[^}]*\}/gm, '').replace(/^\s*#.*$/gm, '');
    expect(outside).not.toMatch(/frame-ancestors|X-Frame-Options/i);
  });
});
