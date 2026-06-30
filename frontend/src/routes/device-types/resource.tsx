// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
import { TypeCapsule } from '@/components/TypeCapsule';
import { TypeAppearanceForm } from '@/components/TypeAppearanceForm';
import {
  listDeviceTypes,
  getDeviceType,
  createDeviceType,
  updateDeviceType,
  deleteDeviceType,
  type DeviceType,
} from '@/lib/api/device-management';

// The device-type registry, described once for the generic list/detail/new pages.
export const deviceTypeResource: RegistryResource<DeviceType> = {
  basePath: '/device-types',
  titlePlural: 'Device Types',
  singular: 'device type',
  backLabel: 'Device types',
  banner: 'devices',
  listDescription: 'Templates that classify devices',
  list: listDeviceTypes,
  load: getDeviceType,
  remove: deleteDeviceType,
  idOf: (dt) => dt.id,
  tokenOf: (dt) => dt.token,
  descriptionOf: (dt) => dt.name ?? '—',
  nameOf: (dt) => dt.name,
  columns: [
    {
      header: 'Appearance',
      cell: (dt) => (
        <TypeCapsule
          appearance={{
            token: dt.token,
            name: dt.name,
            icon: dt.icon,
            backgroundColor: dt.backgroundColor,
            foregroundColor: dt.foregroundColor,
            borderColor: dt.borderColor,
          }}
        />
      ),
    },
    {
      header: 'Token',
      cell: (dt) => <span className="font-mono text-xs text-foreground">{dt.token}</span>,
    },
    { header: 'Description', cell: (dt) => dt.description || '—', className: 'text-muted-foreground' },
    {
      header: 'Created',
      cell: (dt) => (dt.createdAt ? new Date(dt.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
  ],
  renderForm: (dt, onDone) => (
    <RegistryTypeForm
      entity={dt}
      singular="device type"
      tokenPlaceholder="thermostat"
      create={(req) => createDeviceType(req)}
      update={(token, req) =>
        updateDeviceType(token, {
          token: req.token,
          name: req.name,
          description: req.description,
          icon: dt?.icon,
          backgroundColor: dt?.backgroundColor,
          foregroundColor: dt?.foregroundColor,
          borderColor: dt?.borderColor,
        })
      }
      onDone={onDone}
    />
  ),
  removeConfirm: (dt) => `Delete device type “${dt.token}”? This cannot be undone.`,
  detailExtraLabel: 'Appearance',
  renderDetailExtra: (dt, reload) => (
    <TypeAppearanceForm
      entity={dt}
      update={(req) => updateDeviceType(dt.token, req)}
      onSaved={reload}
    />
  ),
};
