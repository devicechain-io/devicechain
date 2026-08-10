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
// Groups of every family are the one uniform EntityGroup, so the projection
// that keeps an edit from erasing its unedited fields is shared too.
import { groupPreserved } from '@/lib/api/device-management';

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
      // RegistryTypeForm calls update only when editing, so g is set. A group
      // update replaces every field it names, so an edit that sent only the name
      // erased the group's appearance and its metadata. (Its membership mode and
      // selector survive — the server never takes those from a request that omits
      // them — so a dynamic group kept working while its metadata did not.)
      update={(token, req) =>
        updateAreaGroup(token, { ...groupPreserved(g!), name: req.name, description: req.description })
      }
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
