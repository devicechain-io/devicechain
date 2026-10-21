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
  // The summary behind the badges was capped; counts are lower bounds.
  countsTruncated: string;
  // The severity words for the badge description.
  severity: Record<string, string>;
}

export const ALARM_STRINGS: Record<'en' | 'es', AlarmStrings> = {
  en: {
    tabsLabel: 'Alarms by device type',
    all: 'All',
    deviceType: 'Device type',
    activeBadge: (n, worst) => `${n} active ${n === 1 ? 'alarm' : 'alarms'}, worst ${worst}`,
    countsTruncated: 'Counts cover the newest active alarms only',
    severity: { CRITICAL: 'Critical', MAJOR: 'Major', MINOR: 'Minor', WARNING: 'Warning', INDETERMINATE: 'Indeterminate' },
  },
  es: {
    tabsLabel: 'Alarmas por tipo de dispositivo',
    all: 'Todas',
    deviceType: 'Tipo de dispositivo',
    activeBadge: (n, worst) => `${n} ${n === 1 ? 'alarma activa' : 'alarmas activas'}, la peor: ${worst}`,
    countsTruncated: 'Los recuentos cubren solo las alarmas activas más recientes',
    severity: { CRITICAL: 'Crítica', MAJOR: 'Mayor', MINOR: 'Menor', WARNING: 'Advertencia', INDETERMINATE: 'Indeterminada' },
  },
};

export function alarmStrings(locale: string | undefined = viewerLocale()): AlarmStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? ALARM_STRINGS.es : ALARM_STRINGS.en;
}
