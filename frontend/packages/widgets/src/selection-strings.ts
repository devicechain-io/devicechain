// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Keep the selector's words with its lazy implementation, outside the shared viewer catalog.
import { viewerLocale } from '@devicechain/widget-sdk';

const SELECTION_STRINGS = {
  en: {
    selectionUnavailable: 'Selection isn’t available here.', noTarget: 'No target slot configured.',
    liveSelection: 'Selection is available on the live dashboard.', selectTarget: (target: string) => `Select ${target}`,
    loading: 'Loading…', noOptions: 'No options', select: 'Select…', optionsFailed: 'Couldn’t load options.',
  },
  es: {
    selectionUnavailable: 'La selección no está disponible aquí.', noTarget: 'No se ha configurado una ranura de destino.',
    liveSelection: 'La selección está disponible en el panel en vivo.', selectTarget: (target: string) => `Seleccionar ${target}`,
    loading: 'Cargando…', noOptions: 'No hay opciones', select: 'Seleccionar…', optionsFailed: 'No se pudieron cargar las opciones.',
  },
  'zh-CN': {
    selectionUnavailable: '此处无法进行选择。', noTarget: '尚未配置目标槽位。',
    liveSelection: '可在实时仪表板中进行选择。', selectTarget: (target: string) => `选择 ${target}`,
    loading: '正在加载…', noOptions: '暂无选项', select: '请选择…', optionsFailed: '无法加载选项。',
  },
};

export function selectionStrings(locale: string | undefined = viewerLocale()) {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? SELECTION_STRINGS.es : language === 'zh' ? SELECTION_STRINGS['zh-CN'] : SELECTION_STRINGS.en;
}
