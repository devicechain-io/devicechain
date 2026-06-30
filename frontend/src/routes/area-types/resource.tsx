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
  titlePlural: 'Area Types',
  singular: 'area type',
  banner: 'areas',
  listDescription: 'Templates that classify areas',
  list: listAreaTypes,
  load: getAreaType,
  remove: deleteAreaType,
  idOf: (at) => at.id,
  tokenOf: (at) => at.token,
  nameOf: (at) => at.name,
  columns: [
    {
      header: 'Appearance',
      cell: (at) => <TypeCapsule appearance={appearanceOf(at)} />,
    },
    tokenColumn<AreaType>(),
    descriptionColumn<AreaType>(),
    createdColumn<AreaType>(),
  ],
  renderForm: (at, onDone) => (
    <RegistryTypeForm
      entity={at}
      singular="area type"
      tokenPlaceholder="building"
      create={(req) => createAreaType(req)}
      update={(token, req) =>
        updateAreaType(token, {
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
  removeConfirm: (at) => `Delete area type “${at.token}”? This cannot be undone.`,
  detailExtraLabel: 'Appearance',
  renderDetailExtra: (at, reload) => (
    <TypeAppearanceForm
      entity={at}
      update={(req) => updateAreaType(at.token, req)}
      onSaved={reload}
    />
  ),
};
