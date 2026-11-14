// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The telemetry table's column headers. The rows themselves are the tenant's own measurement
// names and values, which are never translated.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface TableStrings {
  measurement: string;
  value: string;
  time: string;
}

export const TABLE_STRINGS: Record<'en' | 'es' | 'zh-CN', TableStrings> = {
  en: { measurement: 'Measurement', value: 'Value', time: 'Time' },
  es: { measurement: 'Medición', value: 'Valor', time: 'Hora' },
  'zh-CN': { measurement: '测量项', value: '值', time: '时间' },
};

export function tableStrings(locale: string | undefined = viewerLocale()): TableStrings {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? TABLE_STRINGS.es : language === 'zh' ? TABLE_STRINGS['zh-CN'] : TABLE_STRINGS.en;
}
