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

export const TABLE_STRINGS: Record<'en' | 'es', TableStrings> = {
  en: { measurement: 'Measurement', value: 'Value', time: 'Time' },
  es: { measurement: 'Medición', value: 'Valor', time: 'Hora' },
};

export function tableStrings(locale: string | undefined = viewerLocale()): TableStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? TABLE_STRINGS.es : TABLE_STRINGS.en;
}
