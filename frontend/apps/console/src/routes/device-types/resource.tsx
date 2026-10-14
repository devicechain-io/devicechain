// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { tokenColumn, descriptionColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import { TypeAppearanceForm } from '@/components/TypeAppearanceForm';
import { ProfilePanel } from './ProfilePanel';
import { TypeIdentityForm } from './TypeIdentityForm';
import { DeviceTypeForm } from './DeviceTypeForm';
import {
  listDeviceTypes,
  getDeviceType,
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
  // The form carries the maintenance-window picker, so it is its own component rather than
  // the shared registry type form.
  renderForm: (dt, onDone) => <DeviceTypeForm entity={dt} onDone={onDone} />,
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
