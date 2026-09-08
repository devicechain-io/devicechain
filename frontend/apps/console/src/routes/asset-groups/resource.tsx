// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, groupColumns, type RegistryResource } from '@/components/registry';
import { MembershipPanel } from '@/components/MembershipPanel';
import {
  listAssetGroups,
  getAssetGroup,
  createAssetGroup,
  updateAssetGroup,
  deleteAssetGroup,
  listAssets,
  type AssetGroup,
} from '@/lib/api/assets';
// Groups of every family are the one uniform EntityGroup, so the projection
// that keeps an edit from erasing its unedited fields is shared too.

// The asset-group registry, described once for the generic list/detail/new pages.
export const assetGroupResource: RegistryResource<AssetGroup> = {
  basePath: '/asset-groups',
  i18nKey: 'assetGroup',
  banner: 'assets',
  list: listAssetGroups,
  load: getAssetGroup,
  remove: deleteAssetGroup,
  idOf: (g) => g.id,
  tokenOf: (g) => g.token,
  nameOf: (g) => g.name,
  columns: groupColumns<AssetGroup>(),
  renderForm: (g, onDone) => (
    <RegistryTypeForm
      entity={g}
      i18nKey="assetGroup"
      entityType="asset-group"
      create={(req) => createAssetGroup(req)}
      // 🔴 THE REQUEST NAMES ONLY WHAT THIS FORM EDITS, which is now the whole rule
      // rather than a hazard to work around. An edit used to have to re-send the
      // group's appearance and metadata (through groupPreserved) because a group
      // update replaced every field it named and omitting one erased it. The update
      // is partial now, so an unnamed field is left alone — and re-sending a value
      // the operator never saw would be a write over whatever it has become since.
      update={(token, req) =>
        updateAssetGroup(token, { name: req.name, description: req.description })
      }
      onDone={onDone}
    />
  ),
  detailExtraLabel: 'common:membersTab',
  renderDetailExtra: (g) => (
    <MembershipPanel
      groupType="group"
      groupToken={g.token}
      memberType="asset"
      memberI18nKey="asset"
      loadCandidates={() => listAssets({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
    />
  ),
};
