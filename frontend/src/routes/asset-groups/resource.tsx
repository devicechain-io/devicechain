// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
import {
  listAssetGroups,
  getAssetGroup,
  createAssetGroup,
  updateAssetGroup,
  deleteAssetGroup,
  type AssetGroup,
} from '@/lib/api/assets';

// The asset-group registry, described once for the generic list/detail/new pages.
export const assetGroupResource: RegistryResource<AssetGroup> = {
  basePath: '/asset-groups',
  titlePlural: 'Asset Groups',
  singular: 'asset group',
  backLabel: 'Asset groups',
  banner: 'assets',
  listDescription: 'Collections of assets',
  list: listAssetGroups,
  load: getAssetGroup,
  remove: deleteAssetGroup,
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
      singular="asset group"
      tokenPlaceholder="fleet-a"
      create={(req) => createAssetGroup(req)}
      update={(token, req) => updateAssetGroup(token, req)}
      onDone={onDone}
    />
  ),
  removeConfirm: (g) => `Delete asset group “${g.token}”? This cannot be undone.`,
};
