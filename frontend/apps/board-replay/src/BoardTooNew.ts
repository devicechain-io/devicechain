// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The refusal of a board saved by a newer release, in the page's language. Loaded on demand
// (load.ts imports it dynamically), with its own catalogs, so the words cost the initial load
// nothing until a board is actually refused.

import i18n from './i18n/config';
import en from './i18n/locales/en/refusal.json';
import es from './i18n/locales/es/refusal.json';
import zh from './i18n/locales/zh-CN/refusal.json';

const REFUSAL_CATALOGS: Record<string, { errorBoardTooNew: string }> = { en, es, 'zh-CN': zh };

export default function refusalText(): string {
  return (REFUSAL_CATALOGS[i18n.language] ?? en).errorBoardTooNew;
}
