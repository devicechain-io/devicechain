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
  i18nKey: 'assetType',
  banner: 'assets',
  list: listAssetTypes,
  load: getAssetType,
  remove: deleteAssetType,
  idOf: (at) => at.id,
  tokenOf: (at) => at.token,
  nameOf: (at) => at.name,
  columns: [
    {
      header: 'common:colAppearance',
      cell: (at) => <TypeCapsule appearance={appearanceOf(at)} />,
    },
    tokenColumn<AssetType>(),
    descriptionColumn<AssetType>(),
    createdColumn<AssetType>(),
  ],
  renderForm: (at, onDone) => (
    <RegistryTypeForm
      entity={at}
      i18nKey="assetType"
      entityType="asset-type"
      create={(req) => createAssetType(req)}
      update={(token, req) =>
        // A partial update: this form edits name and description, so it sends name
        // and description. The appearance fields, the imageUrl and the metadata are
        // untouched because they are not mentioned.
        updateAssetType(token, {
          name: req.name,
          description: req.description,
        })
      }
      onDone={onDone}
    />
  ),
  detailExtraLabel: 'common:colAppearance',
  renderDetailExtra: (at, reload) => (
    <TypeAppearanceForm
      entity={at}
      // The appearance form edits icon and colors, and sends only those.
      update={(req) => updateAssetType(at.token, req)}
      onSaved={reload}
    />
  ),
};
