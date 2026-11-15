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

const COMMAND_STRINGS: Record<'en' | 'es' | 'zh-CN', CommandStrings> = {
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
  'zh-CN': {
    send: (label) => `发送 ${label}`, sending: '正在发送…', noCommand: '尚未配置命令。',
    noPermission: '您没有下发命令的权限。', noSeam: '此视图中无法使用命令。', noDevice: '尚未为此命令选择设备。',
    sendFailed: '无法下发命令，请重试。', historyFailed: '无法加载命令历史记录。', loading: '正在加载…', empty: '尚未下发任何命令。',
    structuredHint: (required) => `${required ? '必填的' : ''}结构化参数，此处暂不支持。`,
    structured: '结构化参数，此处暂不支持', required: '必填', number: '必须为数字', integer: '必须为整数',
    minimum: (value) => `必须 ≥ ${value}`, maximum: (value) => `必须 ≤ ${value}`,
    status: { QUEUED: '已排队', SENT: '已发送', HELD: '已暂缓', PARKED: '已搁置', SUCCESSFUL: '成功', FAILED: '失败', TIMEOUT: '超时', EXPIRED: '已过期', CANCELLED: '已取消' },
  },
};

export function commandStrings(locale: string | undefined = viewerLocale()): CommandStrings {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? COMMAND_STRINGS.es : language === 'zh' ? COMMAND_STRINGS['zh-CN'] : COMMAND_STRINGS.en;
}
