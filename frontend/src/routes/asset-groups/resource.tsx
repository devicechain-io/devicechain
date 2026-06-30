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

// The asset-group registry, described once for the generic list/detail/new pages.
export const assetGroupResource: RegistryResource<AssetGroup> = {
  basePath: '/asset-groups',
  titlePlural: 'Asset Groups',
  singular: 'asset group',
  banner: 'assets',
  listDescription: 'Collections of assets',
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
      singular="asset group"
      tokenPlaceholder="fleet-a"
      create={(req) => createAssetGroup(req)}
      update={(token, req) => updateAssetGroup(token, req)}
      onDone={onDone}
    />
  ),
  removeConfirm: (g) => `Delete asset group “${g.token}”? This cannot be undone.`,
  detailExtraLabel: 'Members',
  renderDetailExtra: (g) => (
    <MembershipPanel
      groupType="assetgroup"
      groupToken={g.token}
      memberType="asset"
      memberSingular="asset"
      loadCandidates={() => listAssets({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
    />
  ),
};
