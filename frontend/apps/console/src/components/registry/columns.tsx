// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Shared column builders for registry list tables. Every family's list repeats the
// same token / name / description / created cells; these keep that one definition.
// `header` is an i18n key (the list page resolves it) — these four are the shared
// `common` column atoms.

import type { RegistryColumn } from './types';

export function tokenColumn<T extends { token: string }>(): RegistryColumn<T> {
  return {
    header: 'common:colToken',
    cell: (x) => <span className="font-mono text-xs text-foreground">{x.token}</span>,
  };
}

export function nameColumn<T extends { name?: string | null }>(): RegistryColumn<T> {
  return { header: 'common:colName', cell: (x) => x.name || '—', className: 'font-medium text-foreground' };
}

export function descriptionColumn<T extends { description?: string | null }>(): RegistryColumn<T> {
  return {
    header: 'common:colDescription',
    cell: (x) => x.description || '—',
    className: 'text-muted-foreground',
  };
}

export function createdColumn<T extends { createdAt?: string | null }>(): RegistryColumn<T> {
  return {
    header: 'common:colCreated',
    cell: (x) => (x.createdAt ? new Date(x.createdAt).toLocaleDateString() : '—'),
    className: 'text-muted-foreground',
  };
}

// The standard four columns for a group (token / name / description / created).
export function groupColumns<
  T extends { token: string; name?: string | null; description?: string | null; createdAt?: string | null },
>(): RegistryColumn<T>[] {
  return [tokenColumn<T>(), nameColumn<T>(), descriptionColumn<T>(), createdColumn<T>()];
}
