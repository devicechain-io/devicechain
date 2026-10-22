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

export const HISTORY_STRINGS: Record<'en' | 'es', HistoryStrings> = {
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
};

export function historyStrings(): HistoryStrings {
  return viewerLocale()?.toLowerCase().startsWith('es') ? HISTORY_STRINGS.es : HISTORY_STRINGS.en;
}
