// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, tokenColumn, descriptionColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import { TypeAppearanceForm } from '@/components/TypeAppearanceForm';
import {
  listAreaTypes,
  getAreaType,
  createAreaType,
  updateAreaType,
  areaTypePreserved,
  deleteAreaType,
  type AreaType,
} from '@/lib/api/areas';

// The area-type registry, described once for the generic list/detail/new pages.
export const areaTypeResource: RegistryResource<AreaType> = {
  basePath: '/area-types',
  i18nKey: 'areaType',
  banner: 'areas',
  list: listAreaTypes,
  load: getAreaType,
  remove: deleteAreaType,
  idOf: (at) => at.id,
  tokenOf: (at) => at.token,
  nameOf: (at) => at.name,
  columns: [
    {
      header: 'common:colAppearance',
      cell: (at) => <TypeCapsule appearance={appearanceOf(at)} />,
    },
    tokenColumn<AreaType>(),
    descriptionColumn<AreaType>(),
    createdColumn<AreaType>(),
  ],
  renderForm: (at, onDone) => (
    <RegistryTypeForm
      entity={at}
      i18nKey="areaType"
      entityType="area-type"
      create={(req) => createAreaType(req)}
      update={(token, req) =>
        // RegistryTypeForm calls update only when editing, so at is set. The
        // appearance fields were already carried by hand here; areaTypePreserved
        // adds the imageUrl and metadata that were not, and makes the next field
        // added to the schema a compile error rather than a silent deletion.
        updateAreaType(token, {
          ...areaTypePreserved(at!),
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
      update={(req) => updateAreaType(at.token, { ...areaTypePreserved(at), ...req })}
      onSaved={reload}
    />
  ),
};
