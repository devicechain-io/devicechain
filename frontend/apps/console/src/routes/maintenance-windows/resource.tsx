// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import {
  tokenColumn,
  nameColumn,
  createdColumn,
  type RegistryResource,
} from '@/components/registry';
import {
  listMaintenanceWindows,
  getMaintenanceWindow,
  deleteMaintenanceWindow,
  type MaintenanceWindow,
} from '@/lib/api/maintenance-windows';
import { MaintenanceWindowForm } from './MaintenanceWindowForm';
import { parseSlots } from './slots';

// The maintenance-window registry, described once for the generic list/detail/new pages.
export const maintenanceWindowResource: RegistryResource<MaintenanceWindow> = {
  basePath: '/maintenance-windows',
  i18nKey: 'maintenanceWindow',
  banner: 'devices',
  list: listMaintenanceWindows,
  load: getMaintenanceWindow,
  remove: deleteMaintenanceWindow,
  idOf: (w) => w.id,
  tokenOf: (w) => w.token,
  nameOf: (w) => w.name,
  columns: [
    tokenColumn<MaintenanceWindow>(),
    nameColumn<MaintenanceWindow>(),
    {
      header: 'entities:maintenanceWindowColTimezone',
      cell: (w) => <span className="font-mono text-xs">{w.timezone}</span>,
    },
    {
      header: 'entities:maintenanceWindowColSlots',
      cell: (w) => {
        const slots = parseSlots(w.slots);
        // An em dash rather than 0: a schedule this console cannot read is not an empty one.
        return slots === null ? (
          <span className="text-muted-foreground">—</span>
        ) : (
          <span className="tabular-nums">{slots.length}</span>
        );
      },
    },
    createdColumn<MaintenanceWindow>(),
  ],
  // Keyed on the entity: the form seeds its rows from the entity once.
  renderForm: (w, onDone) => (
    <MaintenanceWindowForm key={w?.id ?? 'new'} entity={w} onDone={onDone} />
  ),
};
