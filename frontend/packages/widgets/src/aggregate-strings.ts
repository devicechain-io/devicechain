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

export const AGGREGATE_STRINGS: Record<'en' | 'es', AggregateStrings> = {
  en: {
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
};

export function aggregateStrings(locale: string | undefined = viewerLocale()): AggregateStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? AGGREGATE_STRINGS.es : AGGREGATE_STRINGS.en;
}
