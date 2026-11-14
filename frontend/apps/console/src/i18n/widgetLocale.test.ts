// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, expect, it } from 'vitest';
import i18n, { i18nReady } from './config';
import en from './locales/en/dashboards.json';
import zh from './locales/zh-CN/dashboards.json';
import '@/i18n/lazy/dashboards';

afterEach(async () => { await i18n.changeLanguage('en'); });

it('loads Chinese widget prose through the real lazy catalog and sets the language widgets read', async () => {
  await i18nReady;
  await i18n.changeLanguage('zh-CN');
  expect(i18n.resolvedLanguage).toBe('zh-CN');
  expect(document.documentElement.lang).toBe('zh-CN');
  expect(i18n.t('dashboards:widgetTypeEntitySelector')).toBe('实体选择器');
  expect(i18n.t('dashboards:timeRangeAbsProblemOrder')).toBe('开始时间必须早于结束时间。');
  expect(i18n.t('dashboards:widgetDerivedFromParentWithSlot', { slotName: 'pump-3' }))
    .toContain('<mono>pump-3</mono>');
  expect(i18n.t('common:back')).toBe('Back'); // Unrelated namespaces retain their English fallback.
});

it('preserves every interpolation and markup token in the Chinese dashboard catalog', () => {
  const tokens = (s: string) => s.match(/{{[^}]+}}|<\/?\w+>/g)?.sort() ?? [];
  for (const [key, value] of Object.entries(en)) {
    const translated = zh[key as keyof typeof zh];
    expect(tokens(translated), key).toEqual(tokens(value));
    expect(translated, key).not.toMatch(/ADR-\d+/);
  }
});
