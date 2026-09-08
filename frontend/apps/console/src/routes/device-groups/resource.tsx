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
      // 🔴 THE REQUEST NAMES ONLY WHAT THIS FORM EDITS, which is now the whole rule
      // rather than a hazard to work around. An edit used to have to re-send the
      // group's appearance and metadata (through groupPreserved) because a group
      // update replaced every field it named and omitting one erased it. The update
      // is partial now, so an unnamed field is left alone — and re-sending a value
      // the operator never saw would be a write over whatever it has become since.
      update={(token, req) =>
        updateDeviceGroup(token, { name: req.name, description: req.description })
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
