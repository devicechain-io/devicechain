// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The admin console's nav labels moved out of the first download into a lazily loaded `adminNav`
// namespace. This pins the move from both sides: the tenant console's `nav` bundle no longer
// carries them (that is the saving), and they resolve, in both locales, once the admin shell's
// catalog module has run. `adminConsole` stays in `nav` because the tenant user menu renders it.

import { describe, expect, it } from 'vitest';
import i18n from '@/i18n/config';
import enNav from '@/i18n/locales/en/nav.json';
import esNav from '@/i18n/locales/es/nav.json';
import { PAGE_TITLES } from '@/routes/admin/AdminLayout';
import { NAV, isLeaf } from '@/routes/admin/AdminSidebar';

const MOVED = [
  'adminPlatform', 'adminTenants', 'adminList', 'adminTiers', 'adminIdentities', 'adminRoles',
  'adminIdentityProviders', 'adminAiProviders', 'adminAiPackaging', 'adminSettings', 'adminBadge',
  'adminTooltip', 'adminDeletions',
];

describe('the adminNav namespace', () => {
  it('is gone from the nav bundle, except adminConsole which the user menu needs', () => {
    for (const k of MOVED) {
      expect(enNav, k).not.toHaveProperty([k]);
      expect(esNav, k).not.toHaveProperty([k]);
    }
    expect(enNav).toHaveProperty(['adminConsole']);
    expect(esNav).toHaveProperty(['adminConsole']);
  });

  it('resolves after the lazy load, in English and Spanish', async () => {
    // The admin shell (AdminLayout/AdminSidebar, imported above) registers it; nothing in
    // config.ts bundles it.
    for (const k of MOVED) expect(i18n.t(`adminNav:${k}`, { lng: 'en' }), k).not.toBe(k);
    expect(i18n.t('adminNav:adminTenants', { lng: 'en' })).toBe('Tenants');
    expect(i18n.t('adminNav:adminBadge', { lng: 'en' })).toBe('Admin');
    await i18n.loadLanguages('es');
    await i18n.loadNamespaces('adminNav');
    expect(i18n.t('adminNav:adminTenants', { lng: 'es' })).toBe('Inquilinos');
  });

  it('labels every admin sidebar entry and page title with a real string', () => {
    const labels = [
      ...NAV.flatMap((n) => (isLeaf(n) ? [n.labelKey] : [n.labelKey, ...n.children.map((c) => c.labelKey)])),
      ...PAGE_TITLES.map((p) => p.key),
    ];
    for (const key of labels) {
      const out = i18n.t(key, { lng: 'en' });
      expect(out, key).not.toBe(key);
      expect(out, key).not.toContain(':');
    }
  });
});
