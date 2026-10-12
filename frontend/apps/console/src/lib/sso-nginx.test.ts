// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The single sign-on return pages carry a one-time code in their address until the app
// strips it. The app's own <meta name="referrer"> is added only once its script runs, so
// the RESPONSE header is what covers every subresource the page loads before that. It
// lives in nginx, which no other test reads: a deleted header line would leave every
// browser test green. This pins it for both apps' return paths.

import { describe, expect, it } from 'vitest';
import NGINX from '../../../../deploy/nginx.conf?raw';

/** The body of the exact-match location for path, or null when there is none. */
function exactLocation(path: string): string | null {
  const escaped = path.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&');
  const m = new RegExp(`location\\s*=\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(NGINX);
  return m ? m[1] : null;
}

describe('the single sign-on return pages are served with no Referer and no caching', () => {
  it.each([
    ['/login/sso/complete', '/index.html'],
    ['/dash/sso/complete', '/dash/index.html'],
  ])('%s', (path, index) => {
    const body = exactLocation(path);
    expect(body, `no exact location for ${path}`).not.toBeNull();
    expect(body).toMatch(/add_header\s+Referrer-Policy\s+"no-referrer"\s+always\s*;/);
    expect(body).toMatch(/add_header\s+Cache-Control\s+"no-store"\s+always\s*;/);
    // Served from this location (so its headers apply), as the right app's index.
    expect(body).toMatch(new RegExp(`try_files\\s+${index.replace(/[./]/g, '\\$&')}\\s+=404\\s*;`));
  });
});
