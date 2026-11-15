// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The words the chart's event markers print: the tooltip and list text, and the notes under the
// chart. The widget packages carry no i18n runtime, so this is the one small per-language table
// (modelled on aggregate-strings.ts). Option labels are translated in the console's catalogs.

import { viewerLocale } from '@devicechain/widget-sdk';

export type EventKind = 'alarm' | 'command';

export interface EventMarkerStrings {
  // The disclosure's summary, with the number of events listed.
  events: (count: number) => string;
  // The disclosure summary of the alarms a series-scoped chart does not draw.
  otherAlarms: (count: number) => string;
  // Said of an alarm in that list when it names no measurement.
  noMeasurement: string;
  // Said of an alarm in that list that is about another measurement.
  aboutMeasurement: (name: string) => string;
  alarm: string;
  command: string;
  severity: Record<string, string>;
  raised: (time: string) => string;
  cleared: (time: string) => string;
  stillActive: string;
  sent: (time: string) => string;
  // A command's delivery status, where it has settled into one; otherwise the raw status.
  outcome: Record<string, string>;
  // Events in the range beyond the cap, exact.
  more: (count: number) => string;
  // A channel the source cannot supply (a recording without it) or failed to read.
  notInRecording: Record<EventKind, string>;
  failed: Record<EventKind, string>;
}

export const EVENT_MARKER_STRINGS: Record<'en' | 'es' | 'zh-CN', EventMarkerStrings> = {
  en: {
    events: (n) => `Events in this range (${n})`,
    otherAlarms: (n) => `Other alarms on this device (${n})`,
    noMeasurement: 'no measurement known',
    aboutMeasurement: (m) => `about ${m}`,
    alarm: 'Alarm',
    command: 'Command',
    severity: { CRITICAL: 'Critical', MAJOR: 'Major', MINOR: 'Minor', WARNING: 'Warning', INDETERMINATE: 'Indeterminate' },
    raised: (t) => `raised ${t}`,
    cleared: (t) => `cleared ${t}`,
    stillActive: 'still active',
    sent: (t) => `sent ${t}`,
    outcome: {
      SUCCESSFUL: 'succeeded',
      FAILED: 'failed',
      TIMEOUT: 'timed out',
      EXPIRED: 'expired',
      CANCELLED: 'cancelled',
      QUEUED: 'queued',
      SENT: 'sent, awaiting reply',
      HELD: 'held',
      PARKED: 'parked',
    },
    more: (n) => `${n} more not shown`,
    notInRecording: { alarm: 'Alarm markers: not in this recording', command: 'Command markers: not in this recording' },
    failed: { alarm: 'Alarm markers could not be loaded', command: 'Command markers could not be loaded' },
  },
  es: {
    events: (n) => `Eventos en este intervalo (${n})`,
    otherAlarms: (n) => `Otras alarmas de este dispositivo (${n})`,
    noMeasurement: 'medición desconocida',
    aboutMeasurement: (m) => `sobre ${m}`,
    alarm: 'Alarma',
    command: 'Comando',
    severity: { CRITICAL: 'Crítica', MAJOR: 'Mayor', MINOR: 'Menor', WARNING: 'Advertencia', INDETERMINATE: 'Indeterminada' },
    raised: (t) => `activada ${t}`,
    cleared: (t) => `despejada ${t}`,
    stillActive: 'sigue activa',
    sent: (t) => `enviado ${t}`,
    outcome: {
      SUCCESSFUL: 'correcto',
      FAILED: 'fallido',
      TIMEOUT: 'sin respuesta a tiempo',
      EXPIRED: 'caducado',
      CANCELLED: 'cancelado',
      QUEUED: 'en cola',
      SENT: 'enviado, esperando respuesta',
      HELD: 'retenido',
      PARKED: 'en espera',
    },
    more: (n) => `${n} más sin mostrar`,
    notInRecording: { alarm: 'Marcadores de alarmas: no están en esta grabación', command: 'Marcadores de comandos: no están en esta grabación' },
    failed: { alarm: 'No se pudieron cargar los marcadores de alarmas', command: 'No se pudieron cargar los marcadores de comandos' },
  },
  'zh-CN': {
    events: (n) => `此范围内的事件（${n}）`, otherAlarms: (n) => `此设备的其他告警（${n}）`,
    noMeasurement: '测量项未知', aboutMeasurement: (m) => `关于 ${m}`, alarm: '告警', command: '命令',
    severity: { CRITICAL: '危急', MAJOR: '严重', MINOR: '次要', WARNING: '警告', INDETERMINATE: '未确定' },
    raised: (t) => `触发于 ${t}`, cleared: (t) => `清除于 ${t}`, stillActive: '仍处于活动状态', sent: (t) => `发送于 ${t}`,
    outcome: { SUCCESSFUL: '成功', FAILED: '失败', TIMEOUT: '超时', EXPIRED: '已过期', CANCELLED: '已取消', QUEUED: '已排队', SENT: '已发送，等待回复', HELD: '已暂缓', PARKED: '已搁置' },
    more: (n) => `另有 ${n} 条未显示`,
    notInRecording: { alarm: '告警标记：此录制中未包含', command: '命令标记：此录制中未包含' },
    failed: { alarm: '无法加载告警标记', command: '无法加载命令标记' },
  },
};

export function eventMarkerStrings(locale: string | undefined = viewerLocale()): EventMarkerStrings {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? EVENT_MARKER_STRINGS.es : language === 'zh' ? EVENT_MARKER_STRINGS['zh-CN'] : EVENT_MARKER_STRINGS.en;
}
