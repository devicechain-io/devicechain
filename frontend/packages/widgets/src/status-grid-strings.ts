// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fixed words the status grid renders itself: the legend's group names and a tile's accessible
// label. The widget packages carry no i18n runtime, so this is the one small per-language table
// (modelled on fleet-strings.ts); option labels are translated in the console's catalogs.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface StatusGridStrings {
  ok: string;
  none: string;
  // No measurement chosen for the tiles.
  chooseMeasurement: string;
  // The severity levels, by name.
  levels: Record<string, string>;
  // "Pump 3: 82 degC, critical"
  tile: (device: string, reading: string, group: string) => string;
}

export const STATUS_GRID_STRINGS: Record<'en' | 'es' | 'zh-CN', StatusGridStrings> = {
  en: {
    ok: 'OK',
    none: 'No reading',
    chooseMeasurement: 'Choose the measurement the tiles show',
    levels: { critical: 'Critical', major: 'Major', minor: 'Minor', warning: 'Warning', info: 'Info', normal: 'OK' },
    tile: (device, reading, group) => `${device}: ${reading}, ${group}`,
  },
  es: {
    ok: 'Correcto',
    none: 'Sin lectura',
    chooseMeasurement: 'Elija la medición que muestran los mosaicos',
    levels: { critical: 'Crítico', major: 'Mayor', minor: 'Menor', warning: 'Aviso', info: 'Info', normal: 'Correcto' },
    tile: (device, reading, group) => `${device}: ${reading}, ${group}`,
  },
  'zh-CN': {
    ok: '正常', none: '暂无读数', chooseMeasurement: '请选择方格显示的测量项',
    levels: { critical: '危急', major: '严重', minor: '次要', warning: '警告', info: '提示', normal: '正常' },
    tile: (device, reading, group) => `${device}：${reading}，${group}`,
  },
};

export function statusGridStrings(locale: string | undefined = viewerLocale()): StatusGridStrings {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? STATUS_GRID_STRINGS.es : language === 'zh' ? STATUS_GRID_STRINGS['zh-CN'] : STATUS_GRID_STRINGS.en;
}
