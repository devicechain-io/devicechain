// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Simplified Chinese locale is SCOPED: it ships the namespaces its config entry lists, and
// every other screen falls back to English. Two registries say which namespaces those are (the
// `namespaces` list in config.ts, which the parity test reads, and the one-module index the
// loader imports), so they are pinned to each other here: a catalog in the index but not the
// list escapes the parity check, and one in the list but not the index never loads.

import { afterEach, describe, expect, it } from 'vitest';

import i18n, { i18nReady, SUPPORTED_LOCALES } from './config';
import zhCatalogs from './locales/zh-CN';

const files = import.meta.glob('./locales/*/*.json', { eager: true }) as Record<
  string,
  { default: Record<string, string> }
>;
const bundle = (locale: string, ns: string) => files[`./locales/${locale}/${ns}.json`]?.default;

const zh = SUPPORTED_LOCALES.find((l) => l.code === 'zh-CN')!;

afterEach(async () => {
  await i18n.changeLanguage('en');
});

describe('the zh-CN catalogs', () => {
  it('the index ships exactly the namespaces the locale declares, each wired to its own file', () => {
    expect(Object.keys(zhCatalogs).sort()).toEqual([...(zh.namespaces ?? [])].sort());
    for (const ns of zh.namespaces ?? []) {
      expect((zhCatalogs as Record<string, unknown>)[ns], `zh-CN/${ns} is wired to another file`).toEqual(
        bundle('zh-CN', ns),
      );
    }
  });

  it.each(['adminSettings', 'devices', 'identities'])('%s keeps every placeholder and markup tag, and is translated', (ns) => {
    const tokens = (s: string) => s.match(/{{[^}]+}}|<\/?\w+\/?>/g)?.sort() ?? [];
    const en = bundle('en', ns)!;
    const translated = bundle('zh-CN', ns)!;
    let chinese = 0;
    for (const [key, value] of Object.entries(en)) {
      expect(tokens(translated[key]), `${ns}:${key}`).toEqual(tokens(value));
      expect(translated[key], `${ns}:${key}`).not.toMatch(/ADR-\d+/);
      if (/[一-鿿]/.test(translated[key])) chinese++;
    }
    // Not a copy of English: nearly every string carries Chinese text (a few are literals).
    expect(chinese / Object.keys(en).length).toBeGreaterThan(0.9);
  });

  it('renders the device, identity and system-settings screens in Chinese through the real loader', async () => {
    await i18nReady;
    await i18n.changeLanguage('zh-CN');
    expect(i18n.resolvedLanguage).toBe('zh-CN');
    expect(i18n.t('identities:title')).toBe('身份');
    expect(i18n.t('identities:notFound', { email: 'a@example.com' })).toBe('未找到身份“a@example.com”。');
    expect(i18n.t('devices:title')).toBe(bundle('zh-CN', 'devices')!.title);
    expect(i18n.t('devices:title')).not.toBe(bundle('en', 'devices')!.title);
    expect(i18n.t('adminSettings:title')).toBe('系统设置');
    expect(i18n.t('adminSettings:settingSavedToast', { key: 'locale.default' })).toBe('设置“locale.default”已保存');
    expect(i18n.t('common:back')).toBe('Back'); // a namespace zh-CN does not ship stays English
  });
});
