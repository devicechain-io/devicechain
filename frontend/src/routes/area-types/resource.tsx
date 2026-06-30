// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
import { TypeCapsule } from '@/components/TypeCapsule';
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
  backLabel: 'Area types',
  banner: 'areas',
  listDescription: 'Templates that classify areas',
  list: listAreaTypes,
  load: getAreaType,
  remove: deleteAreaType,
  idOf: (at) => at.id,
  tokenOf: (at) => at.token,
  descriptionOf: (at) => at.name ?? '—',
  nameOf: (at) => at.name,
  columns: [
    {
      header: 'Appearance',
      cell: (at) => (
        <TypeCapsule
          appearance={{
            token: at.token,
            name: at.name,
            icon: at.icon,
            backgroundColor: at.backgroundColor,
            foregroundColor: at.foregroundColor,
            borderColor: at.borderColor,
          }}
        />
      ),
    },
    {
      header: 'Token',
      cell: (at) => <span className="font-mono text-xs text-foreground">{at.token}</span>,
    },
    { header: 'Description', cell: (at) => at.description || '—', className: 'text-muted-foreground' },
    {
      header: 'Created',
      cell: (at) => (at.createdAt ? new Date(at.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
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
