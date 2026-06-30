// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
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
  listDescription: 'Templates that classify areas',
  list: listAreaTypes,
  load: getAreaType,
  remove: deleteAreaType,
  idOf: (at) => at.id,
  tokenOf: (at) => at.token,
  descriptionOf: (at) => at.name ?? '—',
  columns: [
    {
      header: 'Token',
      cell: (at) => (
        <div className="flex items-center gap-2">
          <span
            className={
              at.backgroundColor ? 'size-4 shrink-0 rounded' : 'size-4 shrink-0 rounded bg-muted'
            }
            style={at.backgroundColor ? { backgroundColor: at.backgroundColor } : undefined}
            aria-hidden
          />
          <span className="font-mono text-xs text-foreground">{at.token}</span>
        </div>
      ),
    },
    { header: 'Name', cell: (at) => at.name || '—', className: 'font-medium text-foreground' },
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
      update={(token, req) => updateAreaType(token, req)}
      onDone={onDone}
    />
  ),
  removeConfirm: (at) => `Delete area type “${at.token}”? This cannot be undone.`,
};
