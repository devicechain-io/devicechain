// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
// Include multiline and optional calls; passing undefined still selects the
// browser locale. This guard is intentionally source-level, like the i18n sweep.
function implicitLocaleCalls(source: string): number[] {
  const call = /(?:\.\s*toLocale(?:Date|Time)?String|\[\s*['"]toLocale(?:Date|Time)?String['"]\s*\])\s*(?:\?\.)?\s*\(\s*(?:\)|undefined\b)/g;
  return [...source.matchAll(call)].map((match) => source.slice(0, match.index).split('\n').length);
}

describe('explicit locale formatting', () => {
  it('detects planted implicit calls but permits explicit locales', () => {
    expect(implicitLocaleCalls('date.toLocaleString();')).toEqual([1]);
    expect(implicitLocaleCalls('date?.toLocaleDateString(\n);')).toEqual([1]);
    expect(implicitLocaleCalls('number["toLocaleString"](undefined);')).toEqual([1]);
    expect(implicitLocaleCalls('date.toLocaleTimeString("es");')).toEqual([]);
  });

  it('requires an explicit locale in every console production source file', () => {
    const sources = import.meta.glob<string>([
      '/src/**/*.{ts,tsx}', '!/src/**/*.test.{ts,tsx}', '!/src/**/__generated__/**',
    ], { query: '?raw', import: 'default', eager: true });
    // Reach controls prevent an empty glob or an accidentally narrowed sweep passing.
    expect(Object.keys(sources)).toContain('/src/lib/utils.ts');
    expect(Object.keys(sources)).toContain('/src/components/VersionsPanel.tsx');
    expect(Object.keys(sources)).toContain('/src/routes/device-profiles/BackfillJobs.tsx');
    const violations = Object.entries(sources).flatMap(([path, source]) =>
      implicitLocaleCalls(source).map((line) => `${path}:${line}: pass the console locale or use lib/format`));
    expect(violations).toEqual([]);
  });
});
