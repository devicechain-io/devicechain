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
  assetTypePreserved,
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
        // RegistryTypeForm calls update only when editing, so at is set. The
        // appearance fields were already carried by hand here; assetTypePreserved
        // adds the imageUrl and metadata that were not, and makes the next field
        // added to the schema a compile error rather than a silent deletion.
        updateAssetType(token, {
          ...assetTypePreserved(at!),
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
      // The appearance tab edits icon + colors; everything else — name,
      // description, imageUrl, metadata — has to be carried, or saving a colour
      // deletes it.
      update={(req) => updateAssetType(at.token, { ...assetTypePreserved(at), ...req })}
      onSaved={reload}
    />
  ),
};
