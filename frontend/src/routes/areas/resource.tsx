// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm, type RegistryResource } from '@/components/registry';
import { Badge } from '@/components/ui/badge';
import {
  listAreas,
  getArea,
  deleteArea,
  createArea,
  updateArea,
  listAreaTypes,
  type Area,
} from '@/lib/api/areas';

export const areaResource: RegistryResource<Area> = {
  basePath: '/areas',
  titlePlural: 'Areas',
  singular: 'area',
  backLabel: 'Areas',
  listDescription: 'Locations or zones you organize by',
  banner: 'areas',
  list: listAreas,
  load: getArea,
  remove: deleteArea,
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
      cell: (a) =>
        a.areaType ? (
          <Badge variant="secondary">{a.areaType.name || a.areaType.token}</Badge>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
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
      singular="area"
      typeLabel="Area type"
      typeSingular="area type"
      tokenPlaceholder="warehouse-1"
      defaultTypeToken={a?.areaType?.token}
      loadTypes={() => listAreaTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
      create={(req) =>
        createArea({
          token: req.token,
          name: req.name,
          description: req.description,
          areaTypeToken: req.typeToken,
        })
      }
      update={(token, req) =>
        updateArea(token, {
          token: req.token,
          name: req.name,
          description: req.description,
          areaTypeToken: req.typeToken,
        })
      }
      onDone={onDone}
    />
  ),
  removeConfirm: (a) => `Delete area “${a.token}”? This cannot be undone.`,
};
