// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The registry "resource kit": a single config object describes a tenant-registry
// entity (device type, asset type, …) and the generic ResourceListPage /
// ResourceDetailPage / ResourceNewPage render the whole list → detail → new flow
// from it. Adding a new entity means writing one RegistryResource + a small form,
// not copying three near-identical page files.

import type { ReactNode } from 'react';

// Matches the Pagination component's prop shape and every *SearchResults
// pagination block, so a resource's `list` can return the API result directly.
export interface PaginationInfo {
  pageStart: number | null;
  pageEnd: number | null;
  totalRecords: number | null;
}

export interface RegistryListResult<T> {
  results: T[];
  pagination: PaginationInfo;
}

export interface RegistryColumn<T> {
  header: string;
  cell: (item: T) => ReactNode;
  /** Optional className on the cell (not the header). */
  className?: string;
}

export interface RegistryResource<T> {
  /** Route base, e.g. "/device-types". The /new and /:token routes hang off it. */
  basePath: string;
  /** Plural list-page title, e.g. "Device Types". */
  titlePlural: string;
  /** Lowercase singular used in prose + buttons, e.g. "device type". */
  singular: string;
  /** Back-link label on detail/new pages, e.g. "Device types". */
  backLabel: string;
  /** Optional list-page description line. */
  listDescription?: string;
  /**
   * Category key for the muted header background texture, e.g. "assets".
   * Set only on top-level categories — leave unset for sub-item resources
   * (types, groups) so they render a plain header.
   */
  banner?: string;

  // ── Data plane ──
  list: (page: { pageNumber: number; pageSize: number }) => Promise<RegistryListResult<T>>;
  load: (token: string) => Promise<T | null>;
  remove: (token: string) => Promise<unknown>;

  idOf: (item: T) => string;
  tokenOf: (item: T) => string;

  // ── Presentation ──
  columns: RegistryColumn<T>[];
  /** Detail-page subtitle (e.g. the entity's name). */
  descriptionOf?: (item: T) => ReactNode;
  /** The create/edit form, shared by the new + detail pages. */
  renderForm: (entity: T | undefined, onDone: (message: string) => void) => ReactNode;
  /** Extra detail-page panels below the form (e.g. a device's state + events). */
  renderDetailExtra?: (item: T) => ReactNode;
  /** Override the delete confirmation prompt. */
  removeConfirm?: (item: T) => string;
}
