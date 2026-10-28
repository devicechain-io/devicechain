// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The top-bar title is resolved from the first path segment through NAV_SECTION_KEY. A
// route missing from it falls back to an English-only humanized segment, which is how the
// es console showed "Command Batches", "Basemap" and "Locale". Every sidebar route must
// have a key, and the key must be the SAME label the sidebar shows for that page.

import { describe, expect, it } from 'vitest';
import en from '@/i18n/locales/en/nav.json';
import es from '@/i18n/locales/es/nav.json';
import { NAV_SECTION_KEY } from '@/routes/AppLayout';
import { navLeaves } from '@/routes/AppSidebar';
import { REGISTRY_RESOURCES } from '@/routes/resources';

const leaves = navLeaves();
const seg0 = (href: string) => href.split('/').filter(Boolean)[0] ?? '';

describe('NAV_SECTION_KEY', () => {
  it('has a key for every sidebar route and every registry resource', () => {
    for (const l of leaves) expect(NAV_SECTION_KEY, l.href).toHaveProperty([seg0(l.href)]);
    for (const r of REGISTRY_RESOURCES) expect(NAV_SECTION_KEY, r.basePath).toHaveProperty([seg0(r.basePath)]);
  });

  it('titles single-page routes with the same label key the sidebar uses', () => {
    for (const l of leaves) {
      if (l.href.split('/').filter(Boolean).length === 1) {
        expect(NAV_SECTION_KEY[seg0(l.href)], l.href).toBe(l.labelKey);
      }
    }
  });

  it('only names keys that exist in both locales', () => {
    for (const key of Object.values(NAV_SECTION_KEY)) {
      expect(en, key).toHaveProperty([key]);
      expect(es, key).toHaveProperty([key]);
    }
  });
});
