// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The few fixed words the level-and-indicator widgets render themselves (the reasons they give
// instead of a value, and the on/off captions). The widget packages carry no i18n runtime, so
// this is the one small per-language table, modelled on the SDK's freshness words: a host that
// adds a runtime later replaces exactly this file. Option labels are translated in the
// console's catalogs, not here.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface LevelStrings {
  // No sample for the bound measurement yet.
  noData: string;
  // A scale widget with no min/max configured.
  setRange: string;
  // max is not above min.
  badRange: string;
  // signal-bars with fewer thresholds than it needs bars.
  setThresholds: string;
  aboveScale: string;
  belowScale: string;
  on: string;
  off: string;
}

export const LEVEL_STRINGS: Record<'en' | 'es', LevelStrings> = {
  en: {
    noData: 'No data',
    setRange: 'Set min and max',
    badRange: 'Max must be above min',
    setThresholds: 'Set at least two thresholds',
    aboveScale: 'Above scale',
    belowScale: 'Below scale',
    on: 'On',
    off: 'Off',
  },
  es: {
    noData: 'Sin datos',
    setRange: 'Defina mín. y máx.',
    badRange: 'El máx. debe superar el mín.',
    setThresholds: 'Defina al menos dos umbrales',
    aboveScale: 'Sobre la escala',
    belowScale: 'Bajo la escala',
    on: 'Encendido',
    off: 'Apagado',
  },
};

export function levelStrings(locale: string | undefined = viewerLocale()): LevelStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? LEVEL_STRINGS.es : LEVEL_STRINGS.en;
}
