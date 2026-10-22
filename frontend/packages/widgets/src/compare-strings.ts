// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fixed words the multi-device compare chart renders itself. The widget packages carry no i18n
// runtime, so this is the one small per-language table (modelled on fleet-strings.ts); the option
// labels are translated in the console's catalogs, not here. Loaded with the compare feed (a lazy
// chunk), never by a board without a compare chart.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface CompareStrings {
  // The group is larger than the lines drawn: say how many, and which ones.
  capped: (shown: number, total: number) => string;
  // The group was larger than what the resolver read: the total is a floor, not the size.
  cappedPage: (shown: number, read: number, total: number) => string;
  // History is a per-bucket average; the live tail is the raw readings.
  bucketed: (bucket: string) => string;
  bucketedTitle: (bucket: string) => string;
  // A threshold line is a reference drawn at a value, never a verdict on an average.
  thresholdNote: string;
  // Some devices' history could not be read: their lines hold only what arrived live.
  historyFailed: (n: number) => string;
  // The same facts for a box too narrow for the sentences above; the full text stays in the title.
  cappedShort: (shown: number, total: number) => string;
  thresholdShort: string;
  historyFailedShort: (n: number) => string;
}

export const COMPARE_STRINGS: Record<'en' | 'es', CompareStrings> = {
  en: {
    capped: (shown, total) => `Showing ${shown} of ${total} devices (the first ${shown}); raise Most lines, up to 12, to see more.`,
    cappedPage: (shown, read, total) =>
      `Showing ${shown} of ${total} devices: the group is larger than the ${read} devices read, and the first ${shown} are drawn.`,
    bucketed: (bucket) => `avg per ${bucket}`,
    bucketedTitle: (bucket) =>
      `Stored history is drawn as the average of each ${bucket} bucket, one point per bucket; readings that arrived live since are drawn as they came, unaveraged.`,
    thresholdNote: 'Thresholds are reference lines; a plotted history point is a bucket average, not a reading.',
    historyFailed: (n) => (n === 1 ? '1 device’s history could not be read; its line starts from live readings.' : `${n} devices’ history could not be read; their lines start from live readings.`),
    cappedShort: (shown, total) => `${shown} of ${total} devices`,
    thresholdShort: 'thresholds are references',
    historyFailedShort: (n) => `${n} unread`,
  },
  es: {
    capped: (shown, total) =>
      `Mostrando ${shown} de ${total} dispositivos (los primeros ${shown}); aumente Máximo de líneas, hasta 12, para ver más.`,
    cappedPage: (shown, read, total) =>
      `Mostrando ${shown} de ${total} dispositivos: el grupo es mayor que los ${read} dispositivos leídos, y se dibujan los primeros ${shown}.`,
    bucketed: (bucket) => `prom. por ${bucket}`,
    bucketedTitle: (bucket) =>
      `El historial almacenado se dibuja como el promedio de cada intervalo de ${bucket}, un punto por intervalo; las lecturas llegadas en vivo desde entonces se dibujan tal cual, sin promediar.`,
    thresholdNote: 'Los umbrales son líneas de referencia; un punto de historial es un promedio por intervalo, no una lectura.',
    historyFailed: (n) =>
      n === 1 ? 'No se pudo leer el historial de 1 dispositivo; su línea parte de lecturas en vivo.' : `No se pudo leer el historial de ${n} dispositivos; sus líneas parten de lecturas en vivo.`,
    cappedShort: (shown, total) => `${shown} de ${total} dispositivos`,
    thresholdShort: 'umbrales son referencias',
    historyFailedShort: (n) => `${n} sin leer`,
  },
};

export function compareStrings(locale: string | undefined = viewerLocale()): CompareStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? COMPARE_STRINGS.es : COMPARE_STRINGS.en;
}
