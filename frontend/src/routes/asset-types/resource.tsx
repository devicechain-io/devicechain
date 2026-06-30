// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
import { TypeCapsule } from '@/components/TypeCapsule';
import { TypeAppearanceForm } from '@/components/TypeAppearanceForm';
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
  banner: 'assets',
  listDescription: 'Templates that classify assets',
  list: listAssetTypes,
  load: getAssetType,
  remove: deleteAssetType,
  idOf: (at) => at.id,
  tokenOf: (at) => at.token,
  descriptionOf: (at) => at.name ?? '—',
  nameOf: (at) => at.name,
  columns: [
    {
      header: 'Appearance',
      cell: (at) => (
        <TypeCapsule
          appearance={{
            token: at.token,
            name: at.name,
            icon: at.icon,
            backgroundColor: at.backgroundColor,
            foregroundColor: at.foregroundColor,
            borderColor: at.borderColor,
          }}
        />
      ),
    },
    {
      header: 'Token',
      cell: (at) => <span className="font-mono text-xs text-foreground">{at.token}</span>,
    },
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
      update={(token, req) =>
        updateAssetType(token, {
          token: req.token,
          name: req.name,
          description: req.description,
          icon: at?.icon,
          backgroundColor: at?.backgroundColor,
          foregroundColor: at?.foregroundColor,
          borderColor: at?.borderColor,
        })
      }
      onDone={onDone}
    />
  ),
  removeConfirm: (at) => `Delete asset type “${at.token}”? This cannot be undone.`,
  detailExtraLabel: 'Appearance',
  renderDetailExtra: (at, reload) => (
    <TypeAppearanceForm
      entity={at}
      update={(req) => updateAssetType(at.token, req)}
      onSaved={reload}
    />
  ),
};
