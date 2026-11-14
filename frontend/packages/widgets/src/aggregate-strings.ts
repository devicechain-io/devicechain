// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The few fixed words the aggregate and categorical widgets render themselves: the name of each
// statistic, and the reasons they give instead of a figure. The widget packages carry no i18n
// runtime, so this is the one small per-language table (modelled on level-strings.ts): a host that
// adds a runtime later replaces exactly this file. Option labels are translated in the console's
// catalogs, not here.

import { viewerLocale } from '@devicechain/widget-sdk';

export const AGGREGATE_STATS = ['avg', 'min', 'max', 'sum', 'count', 'last'] as const;
export type AggregateStat = (typeof AGGREGATE_STATS)[number];

export interface AggregateStrings {
  missingCount: (bucket: string, name: string) => string;
  missingFields: (bucket: string, name: string, count: number, fields: string) => string;
  stats: Record<AggregateStat, string>;
  loading: string;
  // The data source cannot compute aggregates (a replayed recording, say).
  unsupported: string;
  failed: (message: string | undefined) => string;
  // Nothing was read in the span.
  noReadings: (span: string) => string;
  noData: string;
  // The widget's slot is bound to no device.
  unbound: string;
  // The accessible name of a card's trend strip: what it draws and over how long.
  trend: (stat: string, span: string) => string;
  // Categorical charts.
  noCategories: string;
  pieNegative: string;
  pieNoShare: string;
}

export const AGGREGATE_STRINGS: Record<'en' | 'es' | 'zh-CN', AggregateStrings> = {
  en: {
    missingCount: (bucket, name) => `the bucket at ${bucket} for "${name}" carries no reading count`,
    missingFields: (bucket, name, count, fields) => `the bucket at ${bucket} for "${name}" holds ${count} readings but no ${fields}`,
    stats: { avg: 'Average', min: 'Minimum', max: 'Maximum', sum: 'Sum', count: 'Readings', last: 'Latest' },
    loading: 'Loading',
    unsupported: 'This data source cannot compute aggregates',
    failed: (m) => (m ? `Aggregates unavailable: ${m}` : 'Aggregates unavailable'),
    noReadings: (span) => `No readings in the last ${span}`,
    noData: 'No data',
    unbound: 'Choose a device for this widget',
    trend: (stat, span) => `${stat} trend over the last ${span}`,
    noCategories: 'Select the measurements to show',
    pieNegative: 'A pie needs values of zero or more',
    pieNoShare: 'Every value is zero, so there is no share to show',
  },
  es: {
    missingCount: (bucket, name) => `el intervalo de ${bucket} para «${name}» no incluye el número de lecturas`,
    missingFields: (bucket, name, count, fields) => `el intervalo de ${bucket} para «${name}» contiene ${count} lecturas, pero faltan ${fields}`,
    stats: { avg: 'Promedio', min: 'Mínimo', max: 'Máximo', sum: 'Suma', count: 'Lecturas', last: 'Último' },
    loading: 'Cargando',
    unsupported: 'Esta fuente de datos no puede calcular agregados',
    failed: (m) => (m ? `Agregados no disponibles: ${m}` : 'Agregados no disponibles'),
    noReadings: (span) => `Sin lecturas en los últimos ${span}`,
    noData: 'Sin datos',
    unbound: 'Elija un dispositivo para este widget',
    trend: (stat, span) => `Tendencia de ${stat.toLowerCase()} en los últimos ${span}`,
    noCategories: 'Seleccione las mediciones que mostrar',
    pieNegative: 'Un gráfico circular necesita valores de cero o más',
    pieNoShare: 'Todos los valores son cero, no hay proporción que mostrar',
  },
  'zh-CN': {
    missingCount: (bucket, name) => `“${name}”在 ${bucket} 的时间桶缺少读数数量`,
    missingFields: (bucket, name, count, fields) => `“${name}”在 ${bucket} 的时间桶包含 ${count} 条读数，但缺少 ${fields}`,
    stats: { avg: '平均值', min: '最小值', max: '最大值', sum: '总和', count: '读数数量', last: '最新值' },
    loading: '正在加载', unsupported: '此数据源无法计算聚合值',
    failed: (m) => (m ? `聚合数据不可用：${m}` : '聚合数据不可用'),
    noReadings: (span) => `最近 ${span} 内没有读数`, noData: '暂无数据', unbound: '请为此组件选择设备',
    trend: (stat, span) => `最近 ${span} 的${stat}趋势`, noCategories: '请选择要显示的测量项',
    pieNegative: '饼图的值必须大于或等于零', pieNoShare: '所有值均为零，无法显示占比',
  },
};

export function aggregateStrings(locale: string | undefined = viewerLocale()): AggregateStrings {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? AGGREGATE_STRINGS.es : language === 'zh' ? AGGREGATE_STRINGS['zh-CN'] : AGGREGATE_STRINGS.en;
}
