// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, expect, it } from 'vitest';
import i18n from './config';
import en from './locales/en/view.json';
import zh from './locales/zh-CN/view.json';

afterEach(async () => { await i18n.changeLanguage('en'); });

it('selects Chinese viewer controls and exposes the document language to shared widgets', async () => {
  await i18n.changeLanguage('zh-CN');
  expect(i18n.resolvedLanguage).toBe('zh-CN');
  expect(document.documentElement.lang).toBe('zh-CN');
  expect(i18n.t('view:absCustom')).toBe('自定义范围');
  expect(i18n.t('view:pausedAt', { time: '14:30' })).toBe('暂停于 14:30');
  expect(i18n.t('view:linkMissing', { token: 'pump-3', slot: 'device' })).toContain('“pump-3”');
});

it('preserves every interpolation token in the Chinese viewer catalog', () => {
  const tokens = (s: string) => s.match(/{{[^}]+}}/g)?.sort() ?? [];
  for (const [key, value] of Object.entries(en)) {
    const translated = zh[key as keyof typeof zh];
    expect(tokens(translated), key).toEqual(tokens(value));
    expect(translated, key).not.toMatch(/ADR-\d+/);
  }
});
