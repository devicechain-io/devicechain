// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, groupColumns, type RegistryResource } from '@/components/registry';
import { MembershipPanel } from '@/components/MembershipPanel';
import {
  listDeviceGroups,
  getDeviceGroup,
  createDeviceGroup,
  updateDeviceGroup,
  groupPreserved,
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
      // RegistryTypeForm calls update only when editing, so g is set. A group
      // update replaces every field it names, so an edit that sent only the name
      // erased the group's appearance and its metadata. (Its membership mode and
      // selector survive — the server never takes those from a request that omits
      // them — so a dynamic group kept working while its metadata did not.)
      update={(token, req) =>
        updateDeviceGroup(token, { ...groupPreserved(g!), name: req.name, description: req.description })
      }
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
