// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fixed words the device status card and the online doughnut render themselves. The widget
// packages carry no i18n runtime, so this is the one small per-language table (modelled on
// fleet-strings.ts); option labels are translated in the console's catalogs, not here.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface PresenceStrings {
  online: string;
  offline: string;
  // The device has never sent anything, so device-state holds no row for it.
  unknown: string;
  neverSeen: string;
  lastSeen: (when: string) => string;
  failed: string;
  unsupported: string;
  // The card is bound to a slot with nothing chosen in it yet.
  unbound: string;
  // The card is bound to a group: it shows one device.
  oneDevice: string;
  useDoughnut: string;
  // The doughnut's figure and its caption.
  ofTotal: (total: number) => string;
  onlineCaption: string;
  notRead: (n: number) => string;
  noStatusYet: (n: number) => string;
  chartLabel: (online: number, offline: number, unknown: number) => string;
}

export const PRESENCE_STRINGS: Record<'en' | 'es' | 'zh-CN', PresenceStrings> = {
  en: {
    online: 'Online',
    offline: 'Offline',
    unknown: 'No status yet',
    neverSeen: 'Never seen',
    lastSeen: (when) => `Last seen ${when}`,
    failed: 'Status could not be read',
    unsupported: 'This data source cannot read device status',
    unbound: 'Choose a device for this widget',
    oneDevice: 'This card shows one device, and it is bound to a group.',
    useDoughnut: 'Bind it to one device, or show the group in an online / offline doughnut.',
    ofTotal: (total) => `of ${total}`,
    onlineCaption: 'online',
    notRead: (n) => `${n} could not be read`,
    noStatusYet: (n) => `${n} with no status yet`,
    chartLabel: (online, offline, unknown) => `${online} online, ${offline} offline, ${unknown} with no status yet`,
  },
  es: {
    online: 'En línea',
    offline: 'Sin conexión',
    unknown: 'Sin estado todavía',
    neverSeen: 'Nunca visto',
    lastSeen: (when) => `Visto por última vez ${when}`,
    failed: 'No se pudo leer el estado',
    unsupported: 'Esta fuente de datos no puede leer el estado de los dispositivos',
    unbound: 'Elija un dispositivo para este widget',
    oneDevice: 'Esta tarjeta muestra un solo dispositivo y está vinculada a un grupo.',
    useDoughnut: 'Vincúlela a un dispositivo o muestre el grupo en un anillo de conectados / desconectados.',
    ofTotal: (total) => `de ${total}`,
    onlineCaption: 'en línea',
    notRead: (n) => `${n} sin leer`,
    noStatusYet: (n) => `${n} sin estado todavía`,
    chartLabel: (online, offline, unknown) => `${online} en línea, ${offline} sin conexión, ${unknown} sin estado todavía`,
  },
  'zh-CN': {
    online: '在线', offline: '离线', unknown: '暂无状态', neverSeen: '从未上报',
    lastSeen: (when) => `最后上报于 ${when}`, failed: '无法读取状态', unsupported: '此数据源无法读取设备状态',
    unbound: '请为此组件选择设备', oneDevice: '此卡片显示单台设备，但当前绑定的是设备组。',
    useDoughnut: '请将其绑定到单台设备，或使用在线/离线环形图显示此组。',
    ofTotal: (total) => `共 ${total} 台`, onlineCaption: '在线', notRead: (n) => `${n} 台无法读取`,
    noStatusYet: (n) => `${n} 台暂无状态`,
    chartLabel: (online, offline, unknown) => `${online} 台在线，${offline} 台离线，${unknown} 台暂无状态`,
  },
};

export function presenceStrings(locale: string | undefined = viewerLocale()): PresenceStrings {
  const language = locale?.toLowerCase().split('-')[0];
  return language === 'es' ? PRESENCE_STRINGS.es : language === 'zh' ? PRESENCE_STRINGS['zh-CN'] : PRESENCE_STRINGS.en;
}
