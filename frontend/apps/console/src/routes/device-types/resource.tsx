// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, tokenColumn, descriptionColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import { TypeAppearanceForm } from '@/components/TypeAppearanceForm';
import { ProfilePanel } from './ProfilePanel';
import { TypeIdentityForm } from './TypeIdentityForm';
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
  i18nKey: 'deviceType',
  banner: 'devices',
  list: listDeviceTypes,
  load: getDeviceType,
  remove: deleteDeviceType,
  idOf: (dt) => dt.id,
  tokenOf: (dt) => dt.token,
  nameOf: (dt) => dt.name,
  columns: [
    {
      header: 'common:colAppearance',
      cell: (dt) => <TypeCapsule appearance={appearanceOf(dt)} />,
    },
    tokenColumn<DeviceType>(),
    descriptionColumn<DeviceType>(),
    createdColumn<DeviceType>(),
  ],
  renderForm: (dt, onDone) => (
    <RegistryTypeForm
      entity={dt}
      i18nKey="deviceType"
      entityType="device-type"
      create={(req) => createDeviceType(req)}
      update={(token, req) =>
        // RegistryTypeForm only calls update when editing, so dt is always set.
        // Carry forward every field this form doesn't edit (appearance, profile
        // ref, facets); DeviceType update is full-replace. See deviceTypePreserved.
        updateDeviceType(token, {
          ...deviceTypePreserved(dt!),
          name: req.name,
          description: req.description,
        })
      }
      onDone={onDone}
    />
  ),
  detailTabs: [
    {
      value: 'identity',
      label: 'entities:deviceTypeIdentityTab',
      render: (dt, reload) => <TypeIdentityForm entity={dt} onSaved={reload} />,
    },
    {
      value: 'appearance',
      label: 'common:colAppearance',
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
      label: 'entities:deviceTypeProfileTab',
      render: (dt, reload) => <ProfilePanel entity={dt} onChanged={reload} />,
    },
  ],
};
