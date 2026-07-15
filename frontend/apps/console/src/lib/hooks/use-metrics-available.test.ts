// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { isGrafanaHealth } from './use-metrics-available';

const res = (status: number, contentType?: string) =>
  new Response(status === 204 ? null : '{}', {
    status,
    headers: contentType ? { 'content-type': contentType } : {},
  });

describe('isGrafanaHealth', () => {
  it('accepts a real Grafana health response (200 + JSON)', () => {
    expect(isGrafanaHealth(res(200, 'application/json'))).toBe(true);
    // Grafana sends a charset parameter — must still match.
    expect(isGrafanaHealth(res(200, 'application/json; charset=UTF-8'))).toBe(true);
  });

  it('rejects the console SPA fallback (200 text/html) — the H1 false positive', () => {
    // When /grafana is absent the probe falls through to the SPA ingress, which
    // returns 200 index.html. res.ok is true, so content type is what saves us.
    expect(isGrafanaHealth(res(200, 'text/html'))).toBe(false);
    expect(isGrafanaHealth(res(200))).toBe(false);
  });

  it('rejects non-2xx even with a JSON content type', () => {
    expect(isGrafanaHealth(res(503, 'application/json'))).toBe(false);
    expect(isGrafanaHealth(res(404, 'application/json'))).toBe(false);
  });

  it('rejects an opaque redirect (status 0, not ok)', () => {
    expect(isGrafanaHealth(Response.error())).toBe(false);
  });
});
