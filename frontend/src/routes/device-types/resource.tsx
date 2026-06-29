// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { RegistryResource } from '@/components/registry';
import {
  listDeviceTypes,
  getDeviceType,
  deleteDeviceType,
  type DeviceType,
} from '@/lib/api/device-management';
import { DeviceTypeForm } from '@/routes/device-types/DeviceTypeForm';

// The device-type registry, described once for the generic list/detail/new pages.
export const deviceTypeResource: RegistryResource<DeviceType> = {
  basePath: '/device-types',
  titlePlural: 'Device Types',
  singular: 'device type',
  backLabel: 'Device types',
  listDescription: 'Templates that classify devices (requires devicetype:read)',
  list: listDeviceTypes,
  load: getDeviceType,
  remove: deleteDeviceType,
  idOf: (dt) => dt.id,
  tokenOf: (dt) => dt.token,
  descriptionOf: (dt) => dt.name ?? '—',
  columns: [
    {
      header: 'Token',
      cell: (dt) => (
        <div className="flex items-center gap-2">
          <span
            className={
              dt.backgroundColor ? 'size-4 shrink-0 rounded' : 'size-4 shrink-0 rounded bg-muted'
            }
            style={dt.backgroundColor ? { backgroundColor: dt.backgroundColor } : undefined}
            aria-hidden
          />
          <span className="font-mono text-xs text-foreground">{dt.token}</span>
        </div>
      ),
    },
    { header: 'Name', cell: (dt) => dt.name || '—', className: 'font-medium text-foreground' },
    { header: 'Description', cell: (dt) => dt.description || '—', className: 'text-muted-foreground' },
    {
      header: 'Created',
      cell: (dt) => (dt.createdAt ? new Date(dt.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
  ],
  renderForm: (dt, onDone) => <DeviceTypeForm deviceType={dt} onDone={onDone} />,
  removeConfirm: (dt) => `Delete device type “${dt.token}”? This cannot be undone.`,
};
