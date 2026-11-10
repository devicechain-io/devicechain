// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The words the alarm widgets render themselves. The widget packages carry no
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
  rows: {
    severity: string;
    status: string;
    originator: string;
    alarm: string;
    value: string;
    raised: string;
    actions: string;
    active: string;
    acknowledged: string;
    cleared: string;
    loading: string;
    empty: string;
    acknowledge: string;
    clear: string;
    acknowledgeAlarm: (key: string) => string;
    clearAlarm: (key: string) => string;
    failed: string;
    drill: (token: string) => string;
    shown: (shown: number, total: number) => string;
  };
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
    rows: {
      severity: 'Severity',
      status: 'Status',
      originator: 'Originator',
      alarm: 'Alarm',
      value: 'Value',
      raised: 'Raised',
      actions: 'Actions',
      active: 'Active',
      acknowledged: 'Acknowledged',
      cleared: 'Cleared',
      loading: 'Loading…',
      empty: 'No alarms',
      acknowledge: 'Ack',
      clear: 'Clear',
      acknowledgeAlarm: (key) => `Acknowledge alarm ${key}`,
      clearAlarm: (key) => `Clear alarm ${key}`,
      failed: 'Action failed — retry',
      drill: (token) => `Show ${token} in the linked widgets`,
      shown: (shown, total) => `Showing ${shown} of ${total}`,
    },
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
    rows: {
      severity: 'Severidad',
      status: 'Estado',
      originator: 'Originador',
      alarm: 'Alarma',
      value: 'Valor',
      raised: 'Generada',
      actions: 'Acciones',
      active: 'Activa',
      acknowledged: 'Reconocida',
      cleared: 'Despejada',
      loading: 'Cargando…',
      empty: 'No hay alarmas',
      acknowledge: 'Reconocer',
      clear: 'Despejar',
      acknowledgeAlarm: (key) => `Reconocer alarma ${key}`,
      clearAlarm: (key) => `Despejar alarma ${key}`,
      failed: 'La acción falló — inténtalo de nuevo',
      drill: (token) => `Mostrar ${token} en los widgets enlazados`,
      shown: (shown, total) => `Mostrando ${shown} de ${total}`,
    },
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
