// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';

// isGrafanaHealth decides whether a probe response is a genuine Grafana health check
// rather than the console SPA's fallback. Checking res.ok alone is a false positive:
// when /grafana is absent the path falls through to the console ingress, whose nginx
// `try_files ... /index.html` returns a straight 200 text/html. A real Grafana
// /api/health response is application/json, so both must hold. Kept a pure function so
// this (the load-bearing distinction) is unit-testable without a DOM.
export function isGrafanaHealth(res: Response): boolean {
  return res.ok && (res.headers.get('content-type') ?? '').includes('application/json');
}

// useMetricsAvailable reports whether the Grafana metrics UI is reachable, gating the
// operator-only "Metrics" link in the console. Grafana SSO is opt-in (the /grafana
// ingress only exists when the instance was bootstrapped with it), so the link must
// not appear otherwise.
//
// It probes Grafana's public health endpoint (same-origin, so no CORS). Checking
// res.ok alone is NOT enough: when /grafana is absent the path falls through to the
// console's SPA ingress, whose nginx `try_files ... /index.html` returns a straight
// 200 text/html (an internal rewrite, not a redirect) — a false positive. A genuine
// Grafana health response is application/json, so we require that content type.
// redirect:'manual' is belt-and-suspenders (any redirect reads as an opaque, not-ok
// response). Every failure path — network error, non-2xx, wrong content type, abort —
// resolves to unavailable, so the link fails closed.
//
// `enabled` lets a caller (e.g. the tenant console) skip the probe entirely for
// non-operators; when false the result is always false.
export function useMetricsAvailable(enabled = true): boolean {
  const [available, setAvailable] = useState(false);

  useEffect(() => {
    if (!enabled) {
      setAvailable(false);
      return;
    }
    const ctrl = new AbortController();
    fetch('/grafana/api/health', { method: 'GET', redirect: 'manual', signal: ctrl.signal })
      .then((res) => {
        if (ctrl.signal.aborted) return;
        setAvailable(isGrafanaHealth(res));
      })
      .catch(() => {
        if (!ctrl.signal.aborted) setAvailable(false);
      });
    return () => ctrl.abort();
  }, [enabled]);

  return available;
}
