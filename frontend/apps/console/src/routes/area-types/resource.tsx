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
        // A partial update: this form edits name and description, so it sends name
        // and description. The appearance fields, the imageUrl and the metadata are
        // untouched because they are not mentioned.
        updateAreaType(token, {
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
      update={(req) => updateAreaType(at.token, req)}
      onSaved={reload}
    />
  ),
};
