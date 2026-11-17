// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fixed words the history table renders itself and exports with. The widget packages carry no i18n
// runtime, so this is the one small per-language table (modelled on fleet-strings.ts); the option labels
// are translated in the console's catalogs, not here. Loaded with the history table's chunk, never by a
// board without one.

import { viewerLocale } from '@devicechain/widget-sdk';

import { ACCESS_STRINGS } from './access-strings';

export interface HistoryStrings {
  time: string;
  // The export's first heading: the instant is a bucket's START (written ISO 8601, in UTC).
  exportTime: string;
  // "avg per 5m": what every figure in the table is.
  average: (bucket: string) => string;
  averageTitle: (bucket: string) => string;
  loading: string;
  unbound: string;
  forbidden: string;
  unsupported: string;
  failed: string;
  empty: string;
  // A cell for a measurement with no readings in that bucket.
  noReadings: string;
  newestFirst: string;
  oldestFirst: string;
  range: (from: number, to: number, of: number) => string;
  previous: string;
  next: string;
}

export const HISTORY_STRINGS: Record<'en' | 'es' | 'zh-CN', HistoryStrings> = {
  en: {
    time: 'Time',
    exportTime: 'Bucket start (UTC)',
    average: (bucket) => `avg per ${bucket}`,
    averageTitle: (bucket) =>
      `Each figure is the average of the readings in a ${bucket} bucket, not a reading: the time is the start of its bucket.`,
    loading: 'Loading',
    unbound: 'Choose a device for this widget',
    forbidden: ACCESS_STRINGS.en.forbidden,
    unsupported: 'This data source cannot read stored history',
    failed: 'The history could not be read',
    empty: 'No readings in this range',
    noReadings: 'no readings in this bucket',
    newestFirst: 'newest first',
    oldestFirst: 'oldest first',
    range: (from, to, of) => `${from}–${to} of ${of}`,
    previous: 'Previous page',
    next: 'Next page',
  },
  es: {
    time: 'Hora',
    exportTime: 'Inicio del intervalo (UTC)',
    average: (bucket) => `prom. por ${bucket}`,
    averageTitle: (bucket) =>
      `Cada cifra es el promedio de las lecturas de un intervalo de ${bucket}, no una lectura: la hora es el inicio de su intervalo.`,
    loading: 'Cargando',
    unbound: 'Elija un dispositivo para este widget',
    forbidden: ACCESS_STRINGS.es.forbidden,
    unsupported: 'Esta fuente de datos no puede leer el historial almacenado',
    failed: 'No se pudo leer el historial',
    empty: 'No hay lecturas en este intervalo',
    noReadings: 'sin lecturas en este intervalo',
    newestFirst: 'más recientes primero',
    oldestFirst: 'más antiguas primero',
    range: (from, to, of) => `${from}–${to} de ${of}`,
    previous: 'Página anterior',
    next: 'Página siguiente',
  },
  'zh-CN': {
    time: '时间', exportTime: '时间桶起点（UTC）', average: (bucket) => `每 ${bucket} 的平均值`,
    averageTitle: (bucket) => `每个数值是 ${bucket} 时间桶内读数的平均值，而非单次读数；时间为该时间桶的起点。`,
    loading: '正在加载', unbound: '请为此组件选择设备', forbidden: ACCESS_STRINGS['zh-CN'].forbidden,
    unsupported: '此数据源无法读取已存储的历史记录', failed: '无法读取历史记录', empty: '此范围内没有读数',
    noReadings: '此时间桶内没有读数', newestFirst: '最新在前', oldestFirst: '最早在前',
    range: (from, to, of) => `第 ${from}–${to} 项，共 ${of} 项`, previous: '上一页', next: '下一页',
  },
};

export function historyStrings(): HistoryStrings {
  const language = viewerLocale()?.toLowerCase().split('-')[0];
  return language === 'es' ? HISTORY_STRINGS.es : language === 'zh' ? HISTORY_STRINGS['zh-CN'] : HISTORY_STRINGS.en;
}
