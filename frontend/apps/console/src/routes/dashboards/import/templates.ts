// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import alarmsBoard from './templates/alarms-board.json';
import deviceDetail from './templates/device-detail.json';
import fleetOverview from './templates/fleet-overview.json';

// The starter dashboards the "New dashboard" flow offers. Each is an ordinary exported
// definition, entered through the same import pipeline as a pasted file: nothing about a
// template is special-cased, so it is parsed against the widget catalog, offered a binding
// for every unbound slot, and saved as a draft like any import. The name and description
// are i18n keys under the dashboards namespace.
export interface DashboardTemplate {
  id: string;
  nameKey: string;
  descriptionKey: string;
  // The definition as the JSON text the import pipeline reads.
  json: string;
}

const text = (definition: unknown) => JSON.stringify(definition);

export const DASHBOARD_TEMPLATES: readonly DashboardTemplate[] = [
  {
    id: 'fleet-overview',
    nameKey: 'templateFleetOverviewName',
    descriptionKey: 'templateFleetOverviewDescription',
    json: text(fleetOverview),
  },
  {
    id: 'device-detail',
    nameKey: 'templateDeviceDetailName',
    descriptionKey: 'templateDeviceDetailDescription',
    json: text(deviceDetail),
  },
  {
    id: 'alarms-board',
    nameKey: 'templateAlarmsBoardName',
    descriptionKey: 'templateAlarmsBoardDescription',
    json: text(alarmsBoard),
  },
];
