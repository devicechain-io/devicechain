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
  // "state of the average per 5m": what each segment is.
  basis: (bucket: string) => string;
  basisTitle: (bucket: string) => string;
  // A segment's hover: "Running, 14:05 to 15:40"
  segment: (state: string, from: string, to: string) => string;
}

export const STATE_TIMELINE_STRINGS: Record<'en' | 'es', StateTimelineStrings> = {
  en: {
    loading: 'Loading',
    unbound: 'Choose a device for this widget',
    forbidden: ACCESS_STRINGS.en.forbidden,
    unsupported: 'This data source cannot read stored history',
    failed: 'The history could not be read',
    empty: 'No readings in this span',
    baseline: 'Normal',
    noData: 'No readings',
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
    baseline: 'Normal',
    noData: 'Sin lecturas',
    basis: (bucket) => `estado del promedio de cada ${bucket}`,
    basisTitle: (bucket) =>
      `Cada tramo es el estado en que estaba su promedio de ${bucket}, no el instante exacto del cambio: una señal que cambia dentro de un intervalo de ${bucket} se lee como el estado del promedio de ese intervalo.`,
    segment: (state, from, to) => `${state}, de ${from} a ${to}`,
  },
};

export function stateTimelineStrings(): StateTimelineStrings {
  return viewerLocale()?.toLowerCase().startsWith('es') ? STATE_TIMELINE_STRINGS.es : STATE_TIMELINE_STRINGS.en;
}
