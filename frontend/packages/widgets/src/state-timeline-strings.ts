// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fixed words the state timeline renders itself. The widget packages carry no i18n runtime, so
// this is the one small per-language table (modelled on history-strings.ts); option labels are
// translated in the console's catalogs, not here.

import { viewerLocale } from '@devicechain/widget-sdk';

import { ACCESS_STRINGS } from './access-strings';

export interface StateTimelineStrings {
  loading: string;
  unbound: string;
  forbidden: string;
  unsupported: string;
  failed: string;
  empty: string;
  // The neutral stretch below the lowest threshold, and a slot with no readings.
  baseline: string;
  noData: string;
  // The severity levels, by name: what an unlabelled threshold row is called.
  levels: Record<string, string>;
  // A stretch, in a lane's accessible description: "Running 06:00 to 08:00"
  lane: (name: string, shares: string, runs: string) => string;
  // "state of the average per 5m": what each segment is.
  basis: (bucket: string) => string;
  basisTitle: (bucket: string) => string;
  // A segment's hover: "Running, 14:05 to 15:40"
  segment: (state: string, from: string, to: string) => string;
}

export const STATE_TIMELINE_STRINGS: Record<'en' | 'es' | 'zh-CN', StateTimelineStrings> = {
  en: {
    loading: 'Loading',
    unbound: 'Choose a device for this widget',
    forbidden: ACCESS_STRINGS.en.forbidden,
    unsupported: 'This data source cannot read stored history',
    failed: 'The history could not be read',
    empty: 'No readings in this span',
    baseline: 'Normal',
    noData: 'No readings',
    levels: { critical: 'Critical', major: 'Major', minor: 'Minor', warning: 'Warning', info: 'Info', normal: 'Normal' },
    lane: (name, shares, runs) => `${name}: ${shares}. ${runs}`,
    basis: (bucket) => `state of the average per ${bucket}`,
    basisTitle: (bucket) =>
      `Each stretch is the state its ${bucket} average was in, not an exact moment of change: a signal that flips inside a ${bucket} slot reads as the state of that slot's average.`,
    segment: (state, from, to) => `${state}, ${from} to ${to}`,
  },
  es: {
    loading: 'Cargando',
    unbound: 'Elija un dispositivo para este widget',
    forbidden: ACCESS_STRINGS.es.forbidden,
    unsupported: 'Esta fuente de datos no puede leer el historial almacenado',
    failed: 'No se pudo leer el historial',
    empty: 'No hay lecturas en este intervalo',
    baseline: 'Sin incidencias',
    noData: 'Sin lecturas',
    levels: { critical: 'Crítico', major: 'Mayor', minor: 'Menor', warning: 'Aviso', info: 'Información', normal: 'Sin incidencias' },
    lane: (name, shares, runs) => `${name}: ${shares}. ${runs}`,
    basis: (bucket) => `estado del promedio de cada ${bucket}`,
    basisTitle: (bucket) =>
      `Cada tramo es el estado en que estaba su promedio de ${bucket}, no el instante exacto del cambio: una señal que cambia dentro de un intervalo de ${bucket} se lee como el estado del promedio de ese intervalo.`,
    segment: (state, from, to) => `${state}, de ${from} a ${to}`,
  },
  'zh-CN': {
    loading: '正在加载', unbound: '请为此组件选择设备', forbidden: ACCESS_STRINGS['zh-CN'].forbidden,
    unsupported: '此数据源无法读取已存储的历史记录', failed: '无法读取历史记录', empty: '此时段内没有读数',
    baseline: '正常', noData: '暂无读数',
    levels: { critical: '危急', major: '严重', minor: '次要', warning: '警告', info: '提示', normal: '正常' },
    lane: (name, shares, runs) => `${name}：${shares}。${runs}`,
    basis: (bucket) => `每 ${bucket} 平均值的状态`,
    basisTitle: (bucket) => `每段表示其 ${bucket} 平均值所处的状态，而非状态变化的精确时刻：在 ${bucket} 时间桶内变化的信号按该时间桶平均值的状态显示。`,
    segment: (state, from, to) => `${state}，${from} 至 ${to}`,
  },
};

export function stateTimelineStrings(): StateTimelineStrings {
  const language = viewerLocale()?.toLowerCase().split('-')[0];
  return language === 'es' ? STATE_TIMELINE_STRINGS.es : language === 'zh' ? STATE_TIMELINE_STRINGS['zh-CN'] : STATE_TIMELINE_STRINGS.en;
}
