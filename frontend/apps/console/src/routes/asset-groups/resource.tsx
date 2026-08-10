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
import { groupPreserved } from '@/lib/api/device-management';

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
      // RegistryTypeForm calls update only when editing, so g is set. A group
      // update replaces every field it names, so an edit that sent only the name
      // erased the group's appearance and its metadata. (Its membership mode and
      // selector survive — the server never takes those from a request that omits
      // them — so a dynamic group kept working while its metadata did not.)
      update={(token, req) =>
        updateAssetGroup(token, { ...groupPreserved(g!), name: req.name, description: req.description })
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
