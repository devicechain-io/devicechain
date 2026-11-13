// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { viewerLocale } from '@devicechain/widget-sdk';

interface CommandStrings {
  send: (label: string) => string;
  sending: string;
  noCommand: string;
  noPermission: string;
  noSeam: string;
  noDevice: string;
  sendFailed: string;
  historyFailed: string;
  loading: string;
  empty: string;
  structuredHint: (required: boolean) => string;
  structured: string;
  required: string;
  number: string;
  integer: string;
  minimum: (value: number) => string;
  maximum: (value: number) => string;
  status: Record<string, string>;
}

const COMMAND_STRINGS: Record<'en' | 'es', CommandStrings> = {
  en: {
    send: (label) => `Send ${label}`,
    sending: 'Sending…',
    noCommand: 'No command configured.',
    noPermission: 'You don’t have permission to issue commands.',
    noSeam: 'Commands aren’t available in this view.',
    noDevice: 'No device is selected for this command.',
    sendFailed: 'Couldn’t issue the command. Try again.',
    historyFailed: 'Couldn’t load command history.',
    loading: 'Loading…',
    empty: 'No commands issued yet.',
    structuredHint: (required) => `${required ? 'required ' : ''}structured parameter — not supported here.`,
    structured: 'Structured parameter — not supported here',
    required: 'Required',
    number: 'Must be a number',
    integer: 'Must be a whole number',
    minimum: (value) => `Must be ≥ ${value}`,
    maximum: (value) => `Must be ≤ ${value}`,
    status: {
      QUEUED: 'Queued', SENT: 'Sent', HELD: 'Held', PARKED: 'Parked',
      SUCCESSFUL: 'Successful', FAILED: 'Failed', TIMEOUT: 'Timeout',
      EXPIRED: 'Expired', CANCELLED: 'Cancelled',
    },
  },
  es: {
    send: (label) => `Enviar ${label}`,
    sending: 'Enviando…',
    noCommand: 'No hay un comando configurado.',
    noPermission: 'No tienes permiso para enviar comandos.',
    noSeam: 'Los comandos no están disponibles en esta vista.',
    noDevice: 'No hay un dispositivo seleccionado para este comando.',
    sendFailed: 'No se pudo enviar el comando. Inténtalo de nuevo.',
    historyFailed: 'No se pudo cargar el historial de comandos.',
    loading: 'Cargando…',
    empty: 'Aún no se ha enviado ningún comando.',
    structuredHint: (required) => `parámetro estructurado${required ? ' obligatorio' : ''} — no compatible aquí.`,
    structured: 'Parámetro estructurado — no compatible aquí',
    required: 'Obligatorio',
    number: 'Debe ser un número',
    integer: 'Debe ser un número entero',
    minimum: (value) => `Debe ser ≥ ${value}`,
    maximum: (value) => `Debe ser ≤ ${value}`,
    status: {
      QUEUED: 'En cola', SENT: 'Enviado', HELD: 'Retenido', PARKED: 'En espera',
      SUCCESSFUL: 'Exitoso', FAILED: 'Fallido', TIMEOUT: 'Tiempo agotado',
      EXPIRED: 'Caducado', CANCELLED: 'Cancelado',
    },
  },
};

export function commandStrings(locale: string | undefined = viewerLocale()): CommandStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? COMMAND_STRINGS.es : COMMAND_STRINGS.en;
}
