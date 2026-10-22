// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// EntityPicker — a searchable Combobox over one entity kind (device / customer /
// area / asset / device type), backed by that kind's list query. This is what replaces the
// paste-a-raw-token inputs the standalone /dash editor used (ADR-039 amendment:
// authoring in the console gets real pickers). Selection stores the entity TOKEN
// — the value dashboard datasources reference.

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { getDeviceType, listDevices, listDeviceTypes } from '@/lib/api/device-management';
import { listCustomers } from '@/lib/api/customers';
import { listAreas } from '@/lib/api/areas';
import { listAssets } from '@/lib/api/assets';

// The entity kinds a dashboard datasource can point at: a device directly, or one
// of the anchor target types.
export type EntityKind = 'device' | 'customer' | 'area' | 'asset' | 'deviceType';

// Per-kind i18n keys (dashboards namespace) for the picker's placeholder/search/empty
// copy — a lookup rather than string interpolation of the raw `kind` value, since the
// displayed word ("device", "customer", …) needs its own translated form per locale.
const KIND_KEYS: Record<
  EntityKind,
  { select: string; search: string; empty: string; failed: string; unknown?: string }
> = {
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
  // A device type is a catalogue entry a stored dashboard can outlive, so this kind alone names a
  // stored token the list does not hold ("unknown type <token>") rather than showing it bare.
  deviceType: {
    select: 'pickerSelectDeviceType',
    search: 'pickerSearchDeviceTypes',
    empty: 'pickerEmptyDeviceTypes',
    failed: 'pickerFailedDeviceTypes',
    unknown: 'pickerUnknownDeviceType',
  },
};

// A generous single page — dashboards reference a handful of entities and the
// Combobox filters client-side, so one wide page beats paged loading here.
const PAGE_SIZE = 200;

type Named = { token: string; name?: string | null };

// `truncated` is true when the server holds more rows than the one page fetched, so a stored
// token missing from `rows` may simply be on a later page and must not be called unknown.
interface Loaded {
  rows: Named[];
  truncated: boolean;
}

// One loader per kind, each returning {token, name} rows for the Combobox.
async function loadEntities(kind: EntityKind): Promise<Loaded> {
  const opts = { pageNumber: 1, pageSize: PAGE_SIZE };
  switch (kind) {
    case 'device':
      return whole((await listDevices(opts)).results);
    case 'customer':
      return whole((await listCustomers(opts)).results);
    case 'area':
      return whole((await listAreas(opts)).results);
    case 'asset':
      return whole((await listAssets(opts)).results);
    case 'deviceType': {
      // The caller's scope is the server's to apply: this is the tenant catalogue read every
      // member may make (device types are configuration, not device-derived rows).
      const found = await listDeviceTypes(opts);
      const total = found.pagination?.totalRecords;
      return { rows: found.results, truncated: typeof total === 'number' && total > found.results.length };
    }
  }
}

const whole = (rows: Named[]): Loaded => ({ rows, truncated: false });

export function EntityPicker({
  kind,
  value,
  onChange,
  id,
  placeholder,
  ariaLabel,
}: {
  kind: EntityKind;
  value: string;
  onChange: (token: string) => void;
  id?: string;
  placeholder?: string;
  // The accessible name for a picker with no visible label of its own (one of a row of them).
  ariaLabel?: string;
}) {
  const { t } = useTranslation(['dashboards', 'common']);
  const { current, error, loading } = useQuery(() => loadEntities(kind), [kind]);
  // A hand-edited/imported definition (opaque JSON, ADR-039) can carry an out-of-union
  // targetType; loadEntities already degrades it to an empty list rather than throwing
  // (see entity-lister.ts). Fall back to the device copy so the picker renders instead
  // of crashing on `t(undefined.select)`.
  const kindKeys = KIND_KEYS[kind] ?? KIND_KEYS.device;

  // A stored device-type token the first page does not hold is looked up BY TOKEN, so a tenant with
  // more types than one page still sees its name — or learns, from that answer, that it is gone.
  // Only for a truncated read: a read that held every row has already answered.
  const offPage =
    kind === 'deviceType' && value && current?.truncated && !current.rows.some((r) => r.token === value) ? value : '';
  const byToken = useQuery(
    () => (offPage ? getDeviceType(offPage).then((found) => ({ found })) : Promise.resolve(null)),
    [offPage],
  );

  const options = useMemo<ComboboxOption[]>(() => {
    // While a new kind loads, useQuery keeps the PREVIOUS kind's data — surfacing
    // it would let a fast click store a wrong-kind token (e.g. a customer token as
    // an `area` binding). Show nothing but the current value until the fetch lands.
    const rows = current?.rows ?? [];
    const opts: ComboboxOption[] = rows.map((e) => ({
      value: e.token,
      label: e.name || e.token,
      description: e.name ? e.token : undefined,
    }));
    // Keep a currently-bound token selectable even if it isn't in the loaded page
    // (or its entity was since removed) — otherwise editing would silently drop it.
    if (value && !opts.some((o) => o.value === value)) {
      // Name it unknown only on an ANSWER (`current`, never the sticky `data`): a read that held
      // every row, or the by-token lookup of an off-page token. A loading or failed read proves
      // nothing about this token, so it shows bare until one of those answers lands.
      const lookedUp = offPage === value ? byToken.current : undefined;
      if (lookedUp?.found) {
        opts.unshift({ value, label: lookedUp.found.name || value, description: lookedUp.found.name ? value : undefined });
      } else {
        const absent = current !== undefined && (!current.truncated || (lookedUp != null && lookedUp.found === null));
        opts.unshift({ value, label: kindKeys.unknown && absent ? t(kindKeys.unknown, { token: value }) : value });
      }
    }
    return opts;
  }, [current, value, kindKeys, t, offPage, byToken.current]);

  return (
    <Combobox
      id={id}
      ariaLabel={ariaLabel}
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
