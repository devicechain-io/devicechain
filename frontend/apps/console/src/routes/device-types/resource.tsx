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
        // A partial update: this form edits name and description, so it sends name
        // and description. Appearance, the profile reference and the facets are
        // untouched because they are not mentioned.
        updateDeviceType(token, {
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
            // The appearance form edits icon and colors, and sends only those.
            updateDeviceType(dt.token, req)
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
