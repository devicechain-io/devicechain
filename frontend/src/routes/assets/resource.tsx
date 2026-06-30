// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm, type RegistryResource } from '@/components/registry';
import { Badge } from '@/components/ui/badge';
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
  titlePlural: 'Assets',
  singular: 'asset',
  backLabel: 'Assets',
  listDescription: 'Physical or logical things you track',
  list: listAssets,
  load: getAsset,
  remove: deleteAsset,
  idOf: (a) => a.id,
  tokenOf: (a) => a.token,
  descriptionOf: (a) => a.name ?? '—',
  columns: [
    {
      header: 'Token',
      cell: (a) => <span className="font-mono text-xs text-foreground">{a.token}</span>,
    },
    { header: 'Name', cell: (a) => a.name || '—', className: 'font-medium text-foreground' },
    {
      header: 'Type',
      cell: (a) => <Badge variant="secondary">{a.assetType.name || a.assetType.token}</Badge>,
    },
    {
      header: 'Created',
      cell: (a) => (a.createdAt ? new Date(a.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
  ],
  renderForm: (a, onDone) => (
    <RegistryInstanceForm
      entity={a}
      singular="asset"
      typeLabel="Asset type"
      typeSingular="asset type"
      tokenPlaceholder="pump-001"
      defaultTypeToken={a?.assetType.token}
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
  removeConfirm: (a) => `Delete asset “${a.token}”? This cannot be undone.`,
};
