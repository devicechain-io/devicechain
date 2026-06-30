// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
import {
  listAssetTypes,
  getAssetType,
  createAssetType,
  updateAssetType,
  deleteAssetType,
  type AssetType,
} from '@/lib/api/assets';

// The asset-type registry, described once for the generic list/detail/new pages.
export const assetTypeResource: RegistryResource<AssetType> = {
  basePath: '/asset-types',
  titlePlural: 'Asset Types',
  singular: 'asset type',
  backLabel: 'Asset types',
  listDescription: 'Templates that classify assets',
  list: listAssetTypes,
  load: getAssetType,
  remove: deleteAssetType,
  idOf: (at) => at.id,
  tokenOf: (at) => at.token,
  descriptionOf: (at) => at.name ?? '—',
  columns: [
    {
      header: 'Token',
      cell: (at) => (
        <div className="flex items-center gap-2">
          <span
            className={
              at.backgroundColor ? 'size-4 shrink-0 rounded' : 'size-4 shrink-0 rounded bg-muted'
            }
            style={at.backgroundColor ? { backgroundColor: at.backgroundColor } : undefined}
            aria-hidden
          />
          <span className="font-mono text-xs text-foreground">{at.token}</span>
        </div>
      ),
    },
    { header: 'Name', cell: (at) => at.name || '—', className: 'font-medium text-foreground' },
    { header: 'Description', cell: (at) => at.description || '—', className: 'text-muted-foreground' },
    {
      header: 'Created',
      cell: (at) => (at.createdAt ? new Date(at.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
  ],
  renderForm: (at, onDone) => (
    <RegistryTypeForm
      entity={at}
      singular="asset type"
      tokenPlaceholder="pump"
      create={(req) => createAssetType(req)}
      update={(token, req) => updateAssetType(token, req)}
      onDone={onDone}
    />
  ),
  removeConfirm: (at) => `Delete asset type “${at.token}”? This cannot be undone.`,
};
