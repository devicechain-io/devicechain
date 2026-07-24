// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, groupColumns, type RegistryResource } from '@/components/registry';
import { MembershipPanel } from '@/components/MembershipPanel';
import {
  listDeviceGroups,
  getDeviceGroup,
  createDeviceGroup,
  updateDeviceGroup,
  deleteDeviceGroup,
  listDevices,
  type DeviceGroup,
} from '@/lib/api/device-management';

export const deviceGroupResource: RegistryResource<DeviceGroup> = {
  basePath: '/device-groups',
  i18nKey: 'deviceGroup',
  banner: 'devices',
  list: listDeviceGroups,
  load: getDeviceGroup,
  remove: deleteDeviceGroup,
  idOf: (g) => g.id,
  tokenOf: (g) => g.token,
  nameOf: (g) => g.name,
  columns: groupColumns<DeviceGroup>(),
  renderForm: (g, onDone) => (
    <RegistryTypeForm
      entity={g}
      i18nKey="deviceGroup"
      entityType="device-group"
      create={(req) => createDeviceGroup(req)}
      update={(token, req) => updateDeviceGroup(token, req)}
      onDone={onDone}
    />
  ),
  detailExtraLabel: 'common:membersTab',
  renderDetailExtra: (g) => (
    <MembershipPanel
      groupType="group"
      groupToken={g.token}
      memberType="device"
      memberI18nKey="device"
      loadCandidates={() => listDevices({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
    />
  ),
};
