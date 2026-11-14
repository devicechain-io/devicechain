// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { viewerLocale } from '@devicechain/widget-sdk';
import type { AbsoluteLabels, TimeRangeLabels } from './time-range-bar';

export const DEFAULT_ABSOLUTE_LABELS: AbsoluteLabels = {
  custom: 'Custom range',
  from: 'From',
  to: 'To',
  apply: 'Apply',
  cancel: 'Cancel',
  zone: (zone, offset) => `Times are in ${zone} (${offset}). Shared links use UTC.`,
  problem: {
    unparseable: 'Enter both a start and an end.',
    order: 'The start must be before the end.',
    tooShort: 'The window must be at least one minute.',
    tooLong: 'The window can be at most 31 days.',
    future: 'The end cannot be in the future.',
    tooEarly: 'The start is too far in the past.',
  },
  fixed: 'Fixed window',
  backToLive: 'Back to live',
  copyLink: 'Copy link',
  linkCopied: 'Link copied',
  loadFailed: 'The range picker could not be loaded. Try again.',
  dismiss: 'Dismiss',
  ignored: (reason, raw) => `The link's time window was ignored: ${reason} (${raw}). Showing the live board.`,
  incomplete: 'both from and to are required',
  unzoned: 'times must be ISO 8601 with a zone, such as 2026-10-22T02:00:00Z or +02:00',
};

export const DEFAULT_TIME_RANGE_LABELS: TimeRangeLabels = {
  range: 'Time range',
  pause: 'Pause',
  resume: 'Resume',
  paused: 'Paused',
  pausedAt: (time) => `Paused at ${time}`,
  actionsLive: 'commands still send',
};


type CompleteLabels = TimeRangeLabels & { absolute: AbsoluteLabels };

const TIME_RANGE_STRINGS: Record<'en' | 'es' | 'zh-CN', CompleteLabels> = {
  en: { ...DEFAULT_TIME_RANGE_LABELS, absolute: DEFAULT_ABSOLUTE_LABELS },
  "es": {
    range: "Intervalo de tiempo",
    pause: "Pausar",
    resume: "Reanudar",
    paused: "En pausa",
    pausedAt: (time) => `En pausa desde las ${time}`,
    actionsLive: "los comandos se siguen enviando",
    absolute: {
      custom: "Intervalo personalizado",
      from: "Desde",
      to: "Hasta",
      apply: "Aplicar",
      cancel: "Cancelar",
      zone: (zone, offset) => `Las horas están en ${zone} (${offset}). Los enlaces compartidos usan UTC.`,
      fixed: "Intervalo fijo",
      backToLive: "Volver a en vivo",
      copyLink: "Copiar enlace",
      linkCopied: "Enlace copiado",
      loadFailed: "No se pudo cargar el selector de intervalo. Inténtalo de nuevo.",
      dismiss: "Descartar",
      ignored: (reason, raw) => `Se ignoró el intervalo de tiempo del enlace: ${reason} (${raw}). Se muestra el panel en vivo.`,
      incomplete: "se requieren tanto from como to",
      unzoned: "las horas deben ser ISO 8601 con zona horaria, por ejemplo 2026-10-22T02:00:00Z o +02:00",
      problem: {
        unparseable: "Introduce un inicio y un fin.",
        order: "El inicio debe ser anterior al fin.",
        tooShort: "El intervalo debe durar al menos un minuto.",
        tooLong: "El intervalo puede durar como máximo 31 días.",
        future: "El fin no puede estar en el futuro.",
        tooEarly: "El inicio queda demasiado lejos en el pasado.",
      },
    },
  },
  "zh-CN": {
    range: "时间范围",
    pause: "暂停",
    resume: "继续",
    paused: "已暂停",
    pausedAt: (time) => `暂停于 ${time}`,
    actionsLive: "命令仍会发送",
    absolute: {
      custom: "自定义范围",
      from: "开始",
      to: "结束",
      apply: "应用",
      cancel: "取消",
      zone: (zone, offset) => `时间以 ${zone}（${offset}）显示。共享链接使用 UTC。`,
      fixed: "固定时间窗口",
      backToLive: "返回实时视图",
      copyLink: "复制链接",
      linkCopied: "链接已复制",
      loadFailed: "无法加载时间范围选择器，请重试。",
      dismiss: "关闭",
      ignored: (reason, raw) => `已忽略链接的时间窗口：${reason}（${raw}）。当前显示实时仪表板。`,
      incomplete: "必须同时提供 from 和 to",
      unzoned: "时间必须使用带时区的 ISO 8601 格式，例如 2026-10-22T02:00:00Z 或 +02:00",
      problem: {
        unparseable: "请输入开始和结束时间。",
        order: "开始时间必须早于结束时间。",
        tooShort: "时间窗口至少为一分钟。",
        tooLong: "时间窗口最多为 31 天。",
        future: "结束时间不能在未来。",
        tooEarly: "开始时间过早。",
      },
    },
  },
};

export function timeRangeStrings(locale: string | undefined = viewerLocale()): CompleteLabels {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? TIME_RANGE_STRINGS.es : language === 'zh' ? TIME_RANGE_STRINGS['zh-CN'] : TIME_RANGE_STRINGS.en;
}
