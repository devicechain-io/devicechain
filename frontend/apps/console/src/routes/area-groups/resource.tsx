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
  i18nKey: 'areaGroup',
  banner: 'areas',
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
      i18nKey="areaGroup"
      entityType="area-group"
      create={(req) => createAreaGroup(req)}
      update={(token, req) => updateAreaGroup(token, req)}
      onDone={onDone}
    />
  ),
  detailExtraLabel: 'common:membersTab',
  renderDetailExtra: (g) => (
    <MembershipPanel
      groupType="group"
      groupToken={g.token}
      memberType="area"
      memberI18nKey="area"
      loadCandidates={() => listAreas({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
    />
  ),
};
