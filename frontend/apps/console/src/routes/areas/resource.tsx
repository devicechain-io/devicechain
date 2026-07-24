// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm, tokenColumn, nameColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
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
  i18nKey: 'area',
  banner: 'areas',
  list: listAreas,
  load: getArea,
  remove: deleteArea,
  idOf: (a) => a.id,
  tokenOf: (a) => a.token,
  nameOf: (a) => a.name,
  typeOf: (a) => (a.areaType ? appearanceOf(a.areaType) : null),
  columns: [
    tokenColumn<Area>(),
    nameColumn<Area>(),
    {
      header: 'common:colType',
      cell: (a) =>
        a.areaType ? (
          <TypeCapsule appearance={appearanceOf(a.areaType)} />
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    createdColumn<Area>(),
  ],
  renderForm: (a, onDone) => (
    <RegistryInstanceForm
      entity={a}
      i18nKey="area"
      entityType="area"
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
};
