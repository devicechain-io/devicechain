// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// EntityPicker — a searchable Combobox over one entity kind (device / customer /
// area / asset), backed by that kind's list query. This is what replaces the
// paste-a-raw-token inputs the standalone /dash editor used (ADR-039 amendment:
// authoring in the console gets real pickers). Selection stores the entity TOKEN
// — the value dashboard datasources reference.

import { useMemo } from 'react';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { listDevices } from '@/lib/api/device-management';
import { listCustomers } from '@/lib/api/customers';
import { listAreas } from '@/lib/api/areas';
import { listAssets } from '@/lib/api/assets';

// The entity kinds a dashboard datasource can point at: a device directly, or one
// of the anchor target types.
export type EntityKind = 'device' | 'customer' | 'area' | 'asset';

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
  const { data, loading } = useQuery(() => loadEntities(kind), [kind]);

  const options = useMemo<ComboboxOption[]>(() => {
    const rows = data ?? [];
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
  }, [data, value]);

  return (
    <Combobox
      id={id}
      options={options}
      value={value}
      onChange={onChange}
      placeholder={loading ? 'Loading…' : (placeholder ?? `Select ${kind}…`)}
      searchPlaceholder={`Search ${kind}s…`}
      emptyMessage={loading ? 'Loading…' : `No ${kind}s found.`}
    />
  );
}
