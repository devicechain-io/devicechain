// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The words the alarm table's device-type tab strip renders itself. The widget packages carry no
// i18n runtime, so this is the one small per-language table, in the manner of fleet-strings.ts;
// option labels are translated in the console's catalogs, not here.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface AlarmStrings {
  // The tab strip's accessible name.
  tabsLabel: string;
  // The first tab: every alarm in scope.
  all: string;
  // The Type column header, shown on the All tab.
  deviceType: string;
  // A tab's badge, spoken: "3 active alarms, worst Critical".
  activeBadge: (count: number, worst: string) => string;
  // The severity words for the badge description.
  severity: Record<string, string>;
  // The severity breakdown (alarm-severity).
  breakdown: {
    // The list's accessible name, spoken before the counts.
    label: string;
    // The caption under the bar: "7 alarms".
    total: (count: number) => string;
    // The caption of a breakdown set to one device type: "7 alarms on pump devices".
    ofType: (count: number, deviceType: string) => string;
    // One list item, spoken: "Critical: 2".
    item: (severity: string, count: number) => string;
    loading: string;
    // The source returned no per-severity counts (it cannot count by severity).
    unsupported: string;
    // The count read failed.
    failed: string;
  };
}

export const ALARM_STRINGS: Record<'en' | 'es', AlarmStrings> = {
  en: {
    tabsLabel: 'Alarms by device type',
    all: 'All',
    deviceType: 'Device type',
    activeBadge: (n, worst) => `${n} active ${n === 1 ? 'alarm' : 'alarms'}, worst ${worst}`,
    severity: { CRITICAL: 'Critical', MAJOR: 'Major', MINOR: 'Minor', WARNING: 'Warning', INDETERMINATE: 'Indeterminate' },
    breakdown: {
      label: 'Alarms by severity',
      total: (n) => `${n} ${n === 1 ? 'alarm' : 'alarms'}`,
      ofType: (n, type) => `${n} ${n === 1 ? 'alarm' : 'alarms'} on ${type} devices`,
      item: (severity, n) => `${severity}: ${n}`,
      loading: 'Loading alarms…',
      unsupported: 'This data source cannot count alarms by severity.',
      failed: 'The alarm counts could not be read.',
    },
  },
  es: {
    tabsLabel: 'Alarmas por tipo de dispositivo',
    all: 'Todas',
    deviceType: 'Tipo de dispositivo',
    activeBadge: (n, worst) => `${n} ${n === 1 ? 'alarma activa' : 'alarmas activas'}, la peor: ${worst}`,
    severity: { CRITICAL: 'Crítica', MAJOR: 'Mayor', MINOR: 'Menor', WARNING: 'Advertencia', INDETERMINATE: 'Indeterminada' },
    breakdown: {
      label: 'Alarmas por severidad',
      total: (n) => `${n} ${n === 1 ? 'alarma' : 'alarmas'}`,
      ofType: (n, type) => `${n} ${n === 1 ? 'alarma' : 'alarmas'} en dispositivos de tipo ${type}`,
      item: (severity, n) => `${severity}: ${n}`,
      loading: 'Cargando alarmas…',
      unsupported: 'Esta fuente de datos no puede contar alarmas por severidad.',
      failed: 'No se pudieron leer los recuentos de alarmas.',
    },
  },
};

export function alarmStrings(locale: string | undefined = viewerLocale()): AlarmStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? ALARM_STRINGS.es : ALARM_STRINGS.en;
}
