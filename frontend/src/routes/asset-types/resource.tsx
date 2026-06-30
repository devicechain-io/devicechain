// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, tokenColumn, descriptionColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
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
  banner: 'assets',
  listDescription: 'Templates that classify assets',
  list: listAssetTypes,
  load: getAssetType,
  remove: deleteAssetType,
  idOf: (at) => at.id,
  tokenOf: (at) => at.token,
  nameOf: (at) => at.name,
  columns: [
    {
      header: 'Appearance',
      cell: (at) => <TypeCapsule appearance={appearanceOf(at)} />,
    },
    tokenColumn<AssetType>(),
    descriptionColumn<AssetType>(),
    createdColumn<AssetType>(),
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
