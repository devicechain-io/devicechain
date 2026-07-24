// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm, tokenColumn, nameColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import {
  listAssets,
  getAsset,
  deleteAsset,
  createAsset,
  updateAsset,
  listAssetTypes,
  type Asset,
} from '@/lib/api/assets';

// The asset registry, described once for the generic list/detail/new pages.
export const assetResource: RegistryResource<Asset> = {
  basePath: '/assets',
  i18nKey: 'asset',
  banner: 'assets',
  list: listAssets,
  load: getAsset,
  remove: deleteAsset,
  idOf: (a) => a.id,
  tokenOf: (a) => a.token,
  nameOf: (a) => a.name,
  typeOf: (a) => (a.assetType ? appearanceOf(a.assetType) : null),
  columns: [
    tokenColumn<Asset>(),
    nameColumn<Asset>(),
    {
      header: 'common:colType',
      cell: (a) =>
        a.assetType ? (
          <TypeCapsule appearance={appearanceOf(a.assetType)} />
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    createdColumn<Asset>(),
  ],
  renderForm: (a, onDone) => (
    <RegistryInstanceForm
      entity={a}
      i18nKey="asset"
      entityType="asset"
      defaultTypeToken={a?.assetType?.token}
      loadTypes={() => listAssetTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
      create={(req) =>
        createAsset({
          token: req.token,
          name: req.name,
          description: req.description,
          assetTypeToken: req.typeToken,
        })
      }
      update={(token, req) =>
        updateAsset(token, {
          token: req.token,
          name: req.name,
          description: req.description,
          assetTypeToken: req.typeToken,
        })
      }
      onDone={onDone}
    />
  ),
};
