// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The registry "resource kit": a single config object describes a tenant-registry
// entity (device type, asset type, …) and the generic ResourceListPage /
// ResourceDetailPage / ResourceNewPage render the whole list → detail → new flow
// from it. Adding a new entity means writing one RegistryResource + a small form,
// not copying three near-identical page files.

import type { ReactNode } from 'react';
import type { TypeAppearance } from '@/components/TypeCapsule';

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
  /** i18n key for the column header, resolved by the list page against the
   *  `entities` + `common` namespaces (e.g. 'common:colToken', 'common:colName').
   *  A key, NOT display text — so headers localize with the rest of the page. */
  header: string;
  cell: (item: T) => ReactNode;
  /** Optional className on the cell (not the header). */
  className?: string;
}

/** A named detail-page tab beside "Basic". Its render receives the loaded entity
 *  and a reload callback to refresh the page after a save. */
export interface DetailTab<T> {
  /** Stable tab key (unique within the resource). */
  value: string;
  /** i18n key for the tab label (resolved against `entities` + `common`). */
  label: string;
  render: (item: T, reload: () => void) => ReactNode;
}

export interface RegistryResource<T> {
  /** Route base, e.g. "/device-types". The /new and /:token routes hang off it. */
  basePath: string;
  /**
   * The family's key prefix in the `entities` namespace, e.g. "deviceType",
   * "asset", "assetGroup". The generic pages compose it with a fixed suffix set
   * (`${i18nKey}TitlePlural`, `${i18nKey}New`, `${i18nKey}ListEmpty`,
   * `${i18nKey}CreatedToast`, …) to resolve every noun-bearing string. The
   * list-empty suffix is deliberately `ListEmpty`, not `Empty`: `area`+`TypeEmpty`
   * would otherwise collide with `areaType`+`Empty` (both "areaTypeEmpty") and one
   * family would silently render another's string — keep new suffixes collision-safe
   * against every prefix that is itself a prefix of another. Whole
   * sentences live per-family in the catalog — the engine never builds prose by
   * interpolating a noun, so each locale writes grammatical text (gender, case,
   * counters) that a shared "New {noun}" template could not.
   */
  i18nKey: string;
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
  /** Detail-page primary title (the entity's name). Falls back to the token. */
  nameOf?: (item: T) => string | null;
  /** The linked type's appearance, shown as a capsule in an instance's detail
   *  header (e.g. a device's device-type). Omit for types/groups. */
  typeOf?: (item: T) => TypeAppearance | null;
  /** The create/edit form, shared by the new + detail pages. */
  renderForm: (entity: T | undefined, onDone: (message: string) => void) => ReactNode;
  /** Extra detail-page content (e.g. a group's members, a type's appearance).
   *  Receives a reload callback to refresh the page after a save. */
  renderDetailExtra?: (item: T, reload: () => void) => ReactNode;
  /** When set alongside renderDetailExtra, the detail page splits into a "Basic"
   *  tab (the form) and a second tab with this i18n-key label holding the extra
   *  content. */
  detailExtraLabel?: string;
  /** Multiple named detail tabs beside "Basic" (e.g. a device profile's Metrics,
   *  Commands, Alarm Rules, Versions). Takes precedence over renderDetailExtra. */
  detailTabs?: DetailTab<T>[];
}
