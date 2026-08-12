// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The cross-language pin between the client's Area vocabulary and the deployment
// catalog the Helm chart actually renders from.
//
// The console decides whether a feature is available by asking the server which
// functional areas were deployed and comparing against areaKey(area). That
// comparison is only meaningful while both sides speak the same vocabulary: an
// Area whose key names no deployable area would be reported "absent" on EVERY
// instance, silently hiding a working feature — the mirror image of the
// phantom-feature bug the mechanism exists to fix.
//
// It reads the chart's own `$known` list rather than keeping a copy. A hand-kept
// expected list here would be a third catalog (after the Go one in
// dc-k8s/functionalarea and the chart's), and the one that drifts silently,
// because nothing would fail when an area is renamed.
//
// 🔑 The direction of this link matters. TS reading a Go/Helm source file is safe;
// the reverse is not, because `go test` does not invalidate its cache for files
// outside the module, so a cached PASS would replay over a changed frontend.
// Vitest has no such cache.

import { describe, it, expect } from 'vitest';
import { AREAS, areaKey, type Area } from '@devicechain/client';
// Read through the bundler rather than node:fs — this app's tsconfig pulls in no
// Node types, and ?raw is the idiom bundle-boundaries.test.ts already uses.
import HELPERS_TPL from '../../../../../deploy/helm/devicechain/templates/_helpers.tpl?raw';

/** The deployable areas the chart recognises, parsed from its `$known` list. */
function chartKnownAreas(): string[] {
  const line = HELPERS_TPL.split('\n').find((l: string) => l.includes('$known := list'));
  if (!line) throw new Error('no "$known := list" line in the chart helpers');
  return [...line.matchAll(/"([^"]+)"/g)].map((m) => m[1]);
}

describe('the Area vocabulary against the chart catalog', () => {
  it('parses a plausible catalog from the chart', () => {
    // The negative control for every assertion below. A parse that silently
    // returned [] would make the subset check below vacuous — it would "pass"
    // only because there was nothing to compare against.
    const known = chartKnownAreas();
    expect(known.length).toBeGreaterThan(5);
    expect(known).toContain('user-management');
    expect(known).toContain('ai-inference');
    expect(known).not.toContain('user-management/admin'); // planes are not areas
  });

  it('maps every Area to an area the chart can deploy', () => {
    const known = chartKnownAreas();
    for (const area of AREAS) {
      expect({ area, key: areaKey(area) }).toEqual({ area, key: expect.stringMatching(/^[a-z-]+$/) });
      expect(known).toContain(areaKey(area));
    }
  });

  it('keys a plane suffix to its owning area', () => {
    // The reason areaKey is not "strip /admin": there is a third plane suffix.
    expect(areaKey('user-management/admin')).toBe('user-management');
    expect(areaKey('user-management/settings')).toBe('user-management');
    expect(areaKey('ai-inference/admin')).toBe('ai-inference');
    expect(areaKey('outbound-connectors')).toBe('outbound-connectors');
  });

  it('rejects a fabricated area — the control for the check above', () => {
    // Without this, "every Area maps to a known area" could pass because the
    // membership test itself is broken rather than because the vocabulary agrees.
    expect(chartKnownAreas()).not.toContain(areaKey('ai-inferenceX' as Area));
  });
});
