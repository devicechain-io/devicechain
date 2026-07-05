// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, tokenColumn, descriptionColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import { TypeAppearanceForm } from '@/components/TypeAppearanceForm';
import { ProfilePanel } from './ProfilePanel';
import {
  listDeviceTypes,
  getDeviceType,
  createDeviceType,
  updateDeviceType,
  deviceTypePreserved,
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
        // Carry forward every field this form doesn't edit (appearance, profile
        // ref, facets); DeviceType update is full-replace. See deviceTypePreserved.
        updateDeviceType(token, {
          ...(dt ? deviceTypePreserved(dt) : { token }),
          token,
          name: req.name,
          description: req.description,
        })
      }
      onDone={onDone}
    />
  ),
  removeConfirm: (dt) => `Delete device type “${dt.token}”? This cannot be undone.`,
  detailTabs: [
    {
      value: 'appearance',
      label: 'Appearance',
      render: (dt, reload) => (
        <TypeAppearanceForm
          entity={dt}
          update={(req) =>
            // The appearance form edits icon/colors; carry the rest forward.
            updateDeviceType(dt.token, { ...deviceTypePreserved(dt), ...req })
          }
          onSaved={reload}
        />
      ),
    },
    {
      value: 'profile',
      label: 'Profile',
      render: (dt, reload) => <ProfilePanel entity={dt} onChanged={reload} />,
    },
  ],
};
