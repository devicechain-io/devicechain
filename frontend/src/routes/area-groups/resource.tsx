// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, groupColumns, type RegistryResource } from '@/components/registry';
import { MembershipPanel } from '@/components/MembershipPanel';
import {
  listAreaGroups,
  getAreaGroup,
  createAreaGroup,
  updateAreaGroup,
  deleteAreaGroup,
  listAreas,
  type AreaGroup,
} from '@/lib/api/areas';

export const areaGroupResource: RegistryResource<AreaGroup> = {
  basePath: '/area-groups',
  titlePlural: 'Area Groups',
  singular: 'area group',
  banner: 'areas',
  listDescription: 'Collections of areas',
  list: listAreaGroups,
  load: getAreaGroup,
  remove: deleteAreaGroup,
  idOf: (g) => g.id,
  tokenOf: (g) => g.token,
  nameOf: (g) => g.name,
  columns: groupColumns<AreaGroup>(),
  renderForm: (g, onDone) => (
    <RegistryTypeForm
      entity={g}
      singular="area group"
      tokenPlaceholder="campus"
      create={(req) => createAreaGroup(req)}
      update={(token, req) => updateAreaGroup(token, req)}
      onDone={onDone}
    />
  ),
  removeConfirm: (g) => `Delete area group “${g.token}”? This cannot be undone.`,
  detailExtraLabel: 'Members',
  renderDetailExtra: (g) => (
    <MembershipPanel
      groupType="areagroup"
      groupToken={g.token}
      memberType="area"
      memberSingular="area"
      loadCandidates={() => listAreas({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
    />
  ),
};
