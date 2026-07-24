// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// EntityPicker — a searchable Combobox over one entity kind (device / customer /
// area / asset), backed by that kind's list query. This is what replaces the
// paste-a-raw-token inputs the standalone /dash editor used (ADR-039 amendment:
// authoring in the console gets real pickers). Selection stores the entity TOKEN
// — the value dashboard datasources reference.

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { listDevices } from '@/lib/api/device-management';
import { listCustomers } from '@/lib/api/customers';
import { listAreas } from '@/lib/api/areas';
import { listAssets } from '@/lib/api/assets';

// The entity kinds a dashboard datasource can point at: a device directly, or one
// of the anchor target types.
export type EntityKind = 'device' | 'customer' | 'area' | 'asset';

// Per-kind i18n keys (dashboards namespace) for the picker's placeholder/search/empty
// copy — a lookup rather than string interpolation of the raw `kind` value, since the
// displayed word ("device", "customer", …) needs its own translated form per locale.
const KIND_KEYS: Record<EntityKind, { select: string; search: string; empty: string; failed: string }> = {
  device: {
    select: 'pickerSelectDevice',
    search: 'pickerSearchDevices',
    empty: 'pickerEmptyDevices',
    failed: 'pickerFailedDevices',
  },
  customer: {
    select: 'pickerSelectCustomer',
    search: 'pickerSearchCustomers',
    empty: 'pickerEmptyCustomers',
    failed: 'pickerFailedCustomers',
  },
  area: {
    select: 'pickerSelectArea',
    search: 'pickerSearchAreas',
    empty: 'pickerEmptyAreas',
    failed: 'pickerFailedAreas',
  },
  asset: {
    select: 'pickerSelectAsset',
    search: 'pickerSearchAssets',
    empty: 'pickerEmptyAssets',
    failed: 'pickerFailedAssets',
  },
};

// A generous single page — dashboards reference a handful of entities and the
// Combobox filters client-side, so one wide page beats paged loading here.
const PAGE_SIZE = 200;

type Named = { token: string; name?: string | null };

// One loader per kind, each returning {token, name} rows for the Combobox.
async function loadEntities(kind: EntityKind): Promise<Named[]> {
  const opts = { pageNumber: 1, pageSize: PAGE_SIZE };
  switch (kind) {
    case 'device':
      return (await listDevices(opts)).results;
    case 'customer':
      return (await listCustomers(opts)).results;
    case 'area':
      return (await listAreas(opts)).results;
    case 'asset':
      return (await listAssets(opts)).results;
  }
}

export function EntityPicker({
  kind,
  value,
  onChange,
  id,
  placeholder,
}: {
  kind: EntityKind;
  value: string;
  onChange: (token: string) => void;
  id?: string;
  placeholder?: string;
}) {
  const { t } = useTranslation(['dashboards', 'common']);
  const { data, loading, error } = useQuery(() => loadEntities(kind), [kind]);
  // A hand-edited/imported definition (opaque JSON, ADR-039) can carry an out-of-union
  // targetType; loadEntities already degrades it to an empty list rather than throwing
  // (see entity-lister.ts). Fall back to the device copy so the picker renders instead
  // of crashing on `t(undefined.select)`.
  const kindKeys = KIND_KEYS[kind] ?? KIND_KEYS.device;

  const options = useMemo<ComboboxOption[]>(() => {
    // While a new kind loads, useQuery keeps the PREVIOUS kind's data — surfacing
    // it would let a fast click store a wrong-kind token (e.g. a customer token as
    // an `area` binding). Show nothing but the current value until the fetch lands.
    const rows = loading ? [] : (data ?? []);
    const opts: ComboboxOption[] = rows.map((e) => ({
      value: e.token,
      label: e.name || e.token,
      description: e.name ? e.token : undefined,
    }));
    // Keep a currently-bound token selectable even if it isn't in the loaded page
    // (or its entity was since removed) — otherwise editing would silently drop it.
    if (value && !opts.some((o) => o.value === value)) {
      opts.unshift({ value, label: value });
    }
    return opts;
  }, [data, loading, value]);

  return (
    <Combobox
      id={id}
      options={options}
      value={value}
      onChange={onChange}
      placeholder={loading ? t('common:loading') : (placeholder ?? t(kindKeys.select))}
      searchPlaceholder={t(kindKeys.search)}
      // Distinguish a failed load from a genuinely empty tenant so the user doesn't
      // "fix" a widget by rebinding when the real problem was a fetch error.
      emptyMessage={loading ? t('common:loading') : error ? t(kindKeys.failed) : t(kindKeys.empty)}
    />
  );
}
