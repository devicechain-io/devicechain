// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The admin top bar falls back to a generic "Admin" for any route missing from PAGE_TITLES,
// which is how /admin/deletions came to be headed "Admin". Every admin route must have a
// title, and every title key must exist in both locales.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { matchPath } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import en from '@/i18n/locales/en/nav.json';
import es from '@/i18n/locales/es/nav.json';
import { PAGE_TITLES } from '@/routes/admin/AdminLayout';
import { NAV, isLeaf } from '@/routes/admin/AdminSidebar';

const titled = (path: string) => PAGE_TITLES.find((e) => matchPath({ path: e.pattern, end: false }, path));

describe('admin page titles', () => {
  it('titles every sidebar route', () => {
    const leaves = NAV.flatMap((n) => (isLeaf(n) ? [n] : n.children));
    expect(leaves.length).toBeGreaterThan(0);
    for (const l of leaves) expect(titled(l.href), l.href).toBeDefined();
  });

  it('titles every /admin route declared in App.tsx', () => {
    const app = readFileSync(resolve(__dirname, '../../App.tsx'), 'utf8');
    const block = app.slice(app.indexOf('path="/admin"'));
    const paths = [...block.matchAll(/<Route path="([^"]+)"/g)].map((m) => m[1]);
    expect(paths).toContain('deletions');
    for (const p of paths) {
      if (p.startsWith('/') || p === '*') continue;
      expect(titled(`/admin/${p.replace(/:[^/]+/g, 'x')}`), p).toBeDefined();
    }
  });

  it('only names keys that exist in both locales', () => {
    for (const { key } of PAGE_TITLES) {
      expect(en, key).toHaveProperty([key]);
      expect(es, key).toHaveProperty([key]);
    }
  });
});
