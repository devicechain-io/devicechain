// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The words the alarm widgets render themselves. The widget packages carry no
// i18n runtime, so this is the one small per-language table, in the manner of fleet-strings.ts;
// option labels are translated in the console's catalogs, not here.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface AlarmStrings {
  countUnit: (count: number) => string;
  worst: (severity: string) => string;
  countLabel: (count: number, severity: string | undefined) => string;
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
    scrollLabel: string;
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

export const ALARM_STRINGS: Record<'en' | 'es' | 'zh-CN', AlarmStrings> = {
  en: {
    countUnit: (n) => n === 1 ? 'alarm' : 'alarms',
    worst: (severity) => `worst ${severity}`,
    countLabel: (n, severity) => `${n} ${n === 1 ? 'alarm' : 'alarms'}${severity ? `, worst severity ${severity}` : ''}`,
    tabsLabel: 'Alarms by device type',
    all: 'All',
    deviceType: 'Device type',
    activeBadge: (n, worst) => `${n} active ${n === 1 ? 'alarm' : 'alarms'}, worst ${worst}`,
    severity: { CRITICAL: 'Critical', MAJOR: 'Major', MINOR: 'Minor', WARNING: 'Warning', INDETERMINATE: 'Indeterminate' },
    rows: {
      scrollLabel: 'Alarms',
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
    countUnit: (n) => n === 1 ? 'alarma' : 'alarmas',
    worst: (severity) => `la peor: ${severity}`,
    countLabel: (n, severity) => `${n} ${n === 1 ? 'alarma' : 'alarmas'}${severity ? `, mayor severidad: ${severity}` : ''}`,
    tabsLabel: 'Alarmas por tipo de dispositivo',
    all: 'Todas',
    deviceType: 'Tipo de dispositivo',
    activeBadge: (n, worst) => `${n} ${n === 1 ? 'alarma activa' : 'alarmas activas'}, la peor: ${worst}`,
    severity: { CRITICAL: 'Crítica', MAJOR: 'Mayor', MINOR: 'Menor', WARNING: 'Advertencia', INDETERMINATE: 'Indeterminada' },
    rows: {
      scrollLabel: 'Alarmas',
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
  'zh-CN': {
    countUnit: () => '条告警',
    worst: (severity) => `最高严重程度：${severity}`,
    countLabel: (n, severity) => `${n} 条告警${severity ? `，最高严重程度：${severity}` : ''}`,
    tabsLabel: '按设备类型查看告警', all: '全部', deviceType: '设备类型',
    activeBadge: (n, worst) => `${n} 条活动告警，最高严重程度：${worst}`,
    severity: { CRITICAL: '危急', MAJOR: '严重', MINOR: '次要', WARNING: '警告', INDETERMINATE: '未确定' },
    rows: {
      scrollLabel: '告警', severity: '严重程度', status: '状态', originator: '来源', alarm: '告警', value: '值',
      raised: '触发时间', actions: '操作', active: '活动', acknowledged: '已确认', cleared: '已清除',
      loading: '正在加载…', empty: '暂无告警', acknowledge: '确认', clear: '清除',
      acknowledgeAlarm: (key) => `确认告警 ${key}`, clearAlarm: (key) => `清除告警 ${key}`,
      failed: '操作失败，请重试', drill: (token) => `在关联组件中显示 ${token}`,
      shown: (shown, total) => `显示 ${total} 条中的 ${shown} 条`,
    },
    breakdown: {
      label: '按严重程度查看告警', total: (n) => `${n} 条告警`, ofType: (n, type) => `${type} 类型设备上的 ${n} 条告警`,
      item: (severity, n) => `${severity}：${n}`, loading: '正在加载告警…',
      unsupported: '此数据源无法按严重程度统计告警。', failed: '无法读取告警数量。',
    },
  },
};

export function alarmStrings(locale: string | undefined = viewerLocale()): AlarmStrings {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? ALARM_STRINGS.es : language === 'zh' ? ALARM_STRINGS['zh-CN'] : ALARM_STRINGS.en;
}
