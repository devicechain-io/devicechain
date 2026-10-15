// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fixed words the fleet table and the per-device bar chart render themselves: the device
// column, the reason a cell holds no number, the truncation and paging captions. The widget
// packages carry no i18n runtime, so this is the one small per-language table (modelled on
// aggregate-strings.ts); option labels are translated in the console's catalogs, not here.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface FleetStrings {
  device: string;
  loading: string;
  // The reason a cell holds no number (D-5): each is a different fact about the device.
  notReported: string;
  noData: string;
  failed: string;
  // Whole-widget states.
  unbound: string;
  forbidden: string;
  unsupported: string;
  noMembers: string;
  noMeasurement: string;
  stale: string;
  // "Showing 500 of 812 devices": a fleet past what is read must say so.
  truncated: (shown: number, total: number) => string;
  range: (from: number, to: number, of: number) => string;
  previous: string;
  next: string;
  sortBy: (column: string) => string;
  drill: (device: string) => string;
}

export const FLEET_STRINGS: Record<'en' | 'es', FleetStrings> = {
  en: {
    device: 'Device',
    loading: 'Loading',
    notReported: 'does not report this',
    noData: 'no data in range',
    failed: 'could not be read',
    unbound: 'Choose a device group for this widget',
    forbidden: 'You do not have access to this data',
    unsupported: 'This data source cannot list a fleet',
    noMembers: 'No devices in this group',
    noMeasurement: 'Choose the measurement to compare',
    stale: 'stale',
    truncated: (shown, total) => `Showing ${shown} of ${total} devices`,
    range: (from, to, of) => `${from}–${to} of ${of}`,
    previous: 'Previous page',
    next: 'Next page',
    sortBy: (column) => `Sort by ${column}`,
    drill: (device) => `Show ${device} in the linked widgets`,
  },
  es: {
    device: 'Dispositivo',
    loading: 'Cargando',
    notReported: 'no informa esta medición',
    noData: 'sin datos en el rango',
    failed: 'no se pudo leer',
    unbound: 'Elija un grupo de dispositivos para este widget',
    forbidden: 'No tiene acceso a estos datos',
    unsupported: 'Esta fuente de datos no puede listar una flota',
    noMembers: 'No hay dispositivos en este grupo',
    noMeasurement: 'Elija la medición que comparar',
    stale: 'obsoleto',
    truncated: (shown, total) => `Mostrando ${shown} de ${total} dispositivos`,
    range: (from, to, of) => `${from}–${to} de ${of}`,
    previous: 'Página anterior',
    next: 'Página siguiente',
    sortBy: (column) => `Ordenar por ${column}`,
    drill: (device) => `Mostrar ${device} en los widgets enlazados`,
  },
};

export function fleetStrings(locale: string | undefined = viewerLocale()): FleetStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? FLEET_STRINGS.es : FLEET_STRINGS.en;
}
