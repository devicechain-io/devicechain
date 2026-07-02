// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, tokenColumn, descriptionColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
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
  banner: 'devices',
  listDescription: 'Templates that classify devices',
  list: listDeviceTypes,
  load: getDeviceType,
  remove: deleteDeviceType,
  idOf: (dt) => dt.id,
  tokenOf: (dt) => dt.token,
  nameOf: (dt) => dt.name,
  columns: [
    {
      header: 'Appearance',
      cell: (dt) => <TypeCapsule appearance={appearanceOf(dt)} />,
    },
    tokenColumn<DeviceType>(),
    descriptionColumn<DeviceType>(),
    createdColumn<DeviceType>(),
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
