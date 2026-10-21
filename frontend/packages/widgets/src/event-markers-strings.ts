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
  alarm: string;
  command: string;
  severity: Record<string, string>;
  raised: (time: string) => string;
  cleared: (time: string) => string;
  stillActive: string;
  sent: (time: string) => string;
  // A command's delivery status, where it has settled into one; otherwise the raw status.
  outcome: Record<string, string>;
  // Events beyond the cap, exact; and an upper bound when the page read was itself full.
  more: (count: number) => string;
  moreUpTo: (count: number) => string;
  // A channel the source cannot supply (a recording without it) or failed to read.
  notInRecording: Record<EventKind, string>;
  failed: Record<EventKind, string>;
}

export const EVENT_MARKER_STRINGS: Record<'en' | 'es', EventMarkerStrings> = {
  en: {
    events: (n) => `Events in this range (${n})`,
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
    moreUpTo: (n) => `Up to ${n} more not shown`,
    notInRecording: { alarm: 'Alarm markers: not in this recording', command: 'Command markers: not in this recording' },
    failed: { alarm: 'Alarm markers could not be loaded', command: 'Command markers could not be loaded' },
  },
  es: {
    events: (n) => `Eventos en este intervalo (${n})`,
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
    moreUpTo: (n) => `Hasta ${n} más sin mostrar`,
    notInRecording: { alarm: 'Marcadores de alarmas: no están en esta grabación', command: 'Marcadores de comandos: no están en esta grabación' },
    failed: { alarm: 'No se pudieron cargar los marcadores de alarmas', command: 'No se pudieron cargar los marcadores de comandos' },
  },
};

export function eventMarkerStrings(locale: string | undefined = viewerLocale()): EventMarkerStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? EVENT_MARKER_STRINGS.es : EVENT_MARKER_STRINGS.en;
}
