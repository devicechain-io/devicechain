// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { viewerLocale } from '@devicechain/widget-sdk';
import type { UnresolvedWidgetReason } from '@devicechain/dashboards';

const SHARED_STRINGS = {
  en: {
    renderFailed: 'This widget failed to render', reset: 'Reset widget', dismiss: 'Dismiss',
    newerViewer: 'This widget needs a newer viewer', cannotShow: 'This widget cannot be shown here',
    widgetType: (type: string) => `Widget type: ${type}`,
    deviceUnavailable: 'Device unavailable', deviceGone: 'The bound device no longer exists.',
    notForCustomers: 'Not available for customer users', customerBinding: 'This widget is bound to something customer users cannot view.',
    dataUnavailable: 'Data unavailable',
    loading: 'Loading…',
    noImage: 'No image URL',
    naturalEarth: 'Made with Natural Earth',
    locationDenied: 'Location not permitted', locationPermission: 'You do not have permission to view device locations.',
    noMapRuntime: 'Map runtime not configured', mapRuntimeHint: "This host must install MapRuntimeProvider with its bundler's MapLibre worker URL.",
    noDevices: 'No devices selected', noPositions: 'No positions reported',
    bindLocation: 'Bind this widget to a device or anchor and select its location series.',
    positionsEmpty: 'None of the selected devices has reported a position yet.',
    mapUnavailable: 'Map renderer unavailable — positions are shown without a basemap.',
    now: 'Now', currentState: 'Shows the current state: the fixed time window does not apply to this widget.',
    undeclared: (capability: string) => `this widget did not declare the ${capability} interaction`,
    versions: (authored: number, installed: number) => `Saved version: ${authored}; installed version: ${installed}.`,
    unresolved: {
      unregistered: 'This widget is not installed in this viewer.',
      unversioned: 'This widget was saved without version information.',
      'invalid-metadata': 'This widget has invalid version information.',
      'options-version': 'The saved settings version does not match the installed widget.',
      'sdk-incompatible': 'The saved widget SDK version does not match the installed widget.',
    },
  },
  es: {
    renderFailed: 'No se pudo mostrar este widget', reset: 'Restablecer widget', dismiss: 'Cerrar',
    newerViewer: 'Este widget necesita un visor más reciente', cannotShow: 'Este widget no se puede mostrar aquí',
    widgetType: (type: string) => `Tipo de widget: ${type}`,
    deviceUnavailable: 'Dispositivo no disponible', deviceGone: 'El dispositivo vinculado ya no existe.',
    notForCustomers: 'No disponible para usuarios de clientes', customerBinding: 'Este widget está vinculado a un recurso que los usuarios de clientes no pueden ver.',
    dataUnavailable: 'Datos no disponibles',
    loading: 'Cargando…',
    noImage: 'No se ha indicado la URL de la imagen',
    naturalEarth: 'Con datos de Natural Earth',
    locationDenied: 'Ubicación no permitida', locationPermission: 'No tiene permiso para ver las ubicaciones de los dispositivos.',
    noMapRuntime: 'El motor del mapa no está configurado', mapRuntimeHint: 'Esta aplicación debe configurar MapRuntimeProvider con la URL del worker de MapLibre de su empaquetador.',
    noDevices: 'No hay dispositivos seleccionados', noPositions: 'No se han comunicado posiciones',
    bindLocation: 'Vincule este widget a un dispositivo o ancla y seleccione su serie de ubicación.',
    positionsEmpty: 'Ninguno de los dispositivos seleccionados ha comunicado una posición todavía.',
    mapUnavailable: 'El motor del mapa no está disponible; las posiciones se muestran sin mapa base.',
    now: 'Ahora', currentState: 'Muestra el estado actual: la ventana de tiempo fija no se aplica a este widget.',
    undeclared: (capability: string) => `Este widget no declaró la interacción ${capability}`,
    versions: (authored: number, installed: number) => `Versión guardada: ${authored}; versión instalada: ${installed}.`,
    unresolved: {
      unregistered: 'Este widget no está instalado en este visor.',
      unversioned: 'Este widget se guardó sin información de versión.',
      'invalid-metadata': 'Este widget tiene información de versión no válida.',
      'options-version': 'La versión de la configuración guardada no coincide con la del widget instalado.',
      'sdk-incompatible': 'La versión del SDK del widget guardado no coincide con la del widget instalado.',
    },
  },
  'zh-CN': {
    renderFailed: '无法显示此组件', reset: '重置组件', dismiss: '关闭',
    newerViewer: '此组件需要更新版本的查看器', cannotShow: '此处无法显示此组件',
    widgetType: (type: string) => `组件类型：${type}`,
    deviceUnavailable: '设备不可用', deviceGone: '绑定的设备已不存在。',
    notForCustomers: '客户用户无法使用', customerBinding: '此组件绑定的资源不支持客户用户查看。',
    dataUnavailable: '数据不可用',
    loading: '正在加载…',
    noImage: '尚未设置图片 URL',
    naturalEarth: '采用 Natural Earth 数据',
    locationDenied: '无权查看位置', locationPermission: '您没有查看设备位置的权限。',
    noMapRuntime: '尚未配置地图运行环境', mapRuntimeHint: '此应用必须配置 MapRuntimeProvider，并提供打包工具生成的 MapLibre worker URL。',
    noDevices: '尚未选择设备', noPositions: '尚未报告位置',
    bindLocation: '请将此组件绑定到设备或锚点，并选择其位置序列。',
    positionsEmpty: '所选设备均尚未报告位置。',
    mapUnavailable: '地图渲染器不可用，位置将不带底图显示。',
    now: '当前', currentState: '显示当前状态：固定时间窗口不适用于此组件。',
    undeclared: (capability: string) => `此组件未声明 ${capability} 交互`,
    versions: (authored: number, installed: number) => `保存的版本：${authored}；已安装的版本：${installed}。`,
    unresolved: {
      unregistered: '此查看器尚未安装此组件。',
      unversioned: '此组件保存时未包含版本信息。',
      'invalid-metadata': '此组件的版本信息无效。',
      'options-version': '已保存的设置版本与已安装的组件不匹配。',
      'sdk-incompatible': '已保存的组件 SDK 版本与已安装的组件不匹配。',
    },
  },
};

export function sharedStrings(locale: string | undefined = viewerLocale()) {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? SHARED_STRINGS.es : language === 'zh' ? SHARED_STRINGS['zh-CN'] : SHARED_STRINGS.en;
}

export function unresolvedWidgetDetail(reason: UnresolvedWidgetReason, locale: string | undefined = viewerLocale()): string {
  const language = locale?.toLowerCase().split('-')[0];
  if (language !== 'es' && language !== 'zh') return reason.message;
  const words = sharedStrings(locale);
  return [words.unresolved[reason.code], reason.versions && words.versions(reason.versions.authored, reason.versions.installed)]
    .filter(Boolean).join(' ');
}
