// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Simplified Chinese locale ships every namespace its config entry lists (all of them). Two registries say which namespaces those are (the
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

  it.each(zh.namespaces ?? [])('%s keeps every placeholder and markup tag, and is translated', (ns) => {
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

  // One Chinese term per concept across the console AND docs/i18n/zh-CN. These are the variants the
  // catalogs once mixed; each maps to the term that won. Add a row when a new split is unified.
  it.each([
    ['预配配置文件', /设备配置方案/],
    ['制品', /更新包(?!含)/],
    ['分面', /分类属性/],
    ['推出', /更新部署/],
    ['身份提供商', /身份提供方/],
    ['机密', /秘密(?!访问密钥)/],
    ['签名者', /签名方(?!面)/],
  ])('uses %s, never a variant of it', (term, forbidden) => {
    const offenders: string[] = [];
    for (const ns of zh.namespaces ?? []) {
      for (const [key, value] of Object.entries(bundle('zh-CN', ns) ?? {})) {
        if (forbidden.test(value)) offenders.push(`${ns}:${key}`);
      }
    }
    expect(offenders, `use ${term}`).toEqual([]);
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
    expect(i18n.t('common:back')).toBe(bundle('zh-CN', 'common')!.back);
    expect(i18n.t('common:back')).not.toBe('Back');
  });

  it('covers every English namespace, including the OTA updates and facets catalogs', () => {
    const english = Object.keys(files)
      .filter((f) => f.startsWith('./locales/en/'))
      .map((f) => f.replace('./locales/en/', '').replace('.json', ''));
    expect([...(zh.namespaces ?? [])].sort()).toEqual(english.sort());
    expect(zh.namespaces).toContain('updates');
    expect(zh.namespaces).toContain('facets');
  });
});
