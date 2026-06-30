// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
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
  titlePlural: 'Device Groups',
  singular: 'device group',
  backLabel: 'Device groups',
  banner: 'devices',
  listDescription: 'Collections of devices',
  list: listDeviceGroups,
  load: getDeviceGroup,
  remove: deleteDeviceGroup,
  idOf: (g) => g.id,
  tokenOf: (g) => g.token,
  descriptionOf: (g) => g.name ?? '—',
  columns: [
    {
      header: 'Token',
      cell: (g) => <span className="font-mono text-xs text-foreground">{g.token}</span>,
    },
    { header: 'Name', cell: (g) => g.name || '—', className: 'font-medium text-foreground' },
    { header: 'Description', cell: (g) => g.description || '—', className: 'text-muted-foreground' },
    {
      header: 'Created',
      cell: (g) => (g.createdAt ? new Date(g.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
  ],
  renderForm: (g, onDone) => (
    <RegistryTypeForm
      entity={g}
      singular="device group"
      tokenPlaceholder="floor-1"
      create={(req) => createDeviceGroup(req)}
      update={(token, req) => updateDeviceGroup(token, req)}
      onDone={onDone}
    />
  ),
  removeConfirm: (g) => `Delete device group “${g.token}”? This cannot be undone.`,
  detailExtraLabel: 'Members',
  renderDetailExtra: (g) => (
    <MembershipPanel
      groupType="devicegroup"
      groupToken={g.token}
      memberType="device"
      memberSingular="device"
      loadCandidates={() => listDevices({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
    />
  ),
};
