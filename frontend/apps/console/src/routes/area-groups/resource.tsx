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
      // 🔴 THE REQUEST NAMES ONLY WHAT THIS FORM EDITS, which is now the whole rule
      // rather than a hazard to work around. An edit used to have to re-send the
      // group's appearance and metadata (through groupPreserved) because a group
      // update replaced every field it named and omitting one erased it. The update
      // is partial now, so an unnamed field is left alone — and re-sending a value
      // the operator never saw would be a write over whatever it has become since.
      update={(token, req) =>
        updateAreaGroup(token, { name: req.name, description: req.description })
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
