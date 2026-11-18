// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// EntityPicker — a searchable Combobox over one entity kind (device / customer /
// area / asset / device type), backed by that kind's list query. This is what replaces the
// paste-a-raw-token inputs the standalone /dash editor used (ADR-039 amendment:
// authoring in the console gets real pickers). Selection stores the entity TOKEN
// — the value dashboard datasources reference.

import { listAll } from '@/lib/api/list-all';
import { createContext, useContext, useMemo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { listDevices, listDeviceTypes } from '@/lib/api/device-management';
import { listCustomers } from '@/lib/api/customers';
import { listAreas } from '@/lib/api/areas';
import { listAssets } from '@/lib/api/assets';
import '@/i18n/lazy/dashboards';

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

type Named = { token: string; name?: string | null };

interface Loaded {
  rows: Named[];
}

// One loader per kind, each returning {token, name} rows for the Combobox.
async function loadEntities(kind: EntityKind): Promise<Loaded> {
  switch (kind) {
    case 'device':
      return whole((await listAll(page => listDevices(page))).results);
    case 'customer':
      return whole((await listAll(page => listCustomers(page))).results);
    case 'area':
      return whole((await listAll(page => listAreas(page))).results);
    case 'asset':
      return whole((await listAll(page => listAssets(page))).results);
    case 'deviceType': {
      // The caller's scope is the server's to apply: this is the tenant catalogue read every
      // member may make (device types are configuration, not device-derived rows).
      const found = await listAll(page => listDeviceTypes(page));
      return whole(found.results);
    }
  }
}

const whole = (rows: Named[]): Loaded => ({ rows });

// What a picker needs of its kind's catalogue read: the answer to the current question (`current`,
// never the sticky display data), a failure, and whether it is in flight.
interface Catalogue {
  current: Loaded | undefined;
  error: string | null;
  loading: boolean;
}

// The device-type catalogue read once for a group of pickers. A repeated list control holds one
// picker per row, and each would otherwise fetch the same page; a picker under this provider reads
// the shared answer instead. Pickers outside one fetch for themselves, as before.
const DeviceTypeCatalogue = createContext<Catalogue | null>(null);

export function SharedDeviceTypeCatalogue({ children }: { children: ReactNode }) {
  const { current, error, loading } = useQuery(() => loadEntities('deviceType'), []);
  const value = useMemo<Catalogue>(() => ({ current, error, loading }), [current, error, loading]);
  return <DeviceTypeCatalogue.Provider value={value}>{children}</DeviceTypeCatalogue.Provider>;
}

interface PickerProps {
  kind: EntityKind;
  value: string;
  onChange: (token: string) => void;
  id?: string;
  placeholder?: string;
  // The accessible name for a picker with no visible label of its own (one of a row of them).
  ariaLabel?: string;
}

export function EntityPicker(props: PickerProps) {
  const shared = useContext(DeviceTypeCatalogue);
  return props.kind === 'deviceType' && shared ? (
    <PickerBody {...props} catalogue={shared} />
  ) : (
    <FetchingPicker {...props} />
  );
}

function FetchingPicker(props: PickerProps) {
  const { current, error, loading } = useQuery(() => loadEntities(props.kind), [props.kind]);
  return <PickerBody {...props} catalogue={{ current, error, loading }} />;
}

function PickerBody({
  kind,
  value,
  onChange,
  id,
  placeholder,
  ariaLabel,
  catalogue: { current, error, loading },
}: PickerProps & { catalogue: Catalogue }) {
  const { t } = useTranslation(['dashboards', 'common']);
  // A hand-edited/imported definition (opaque JSON, ADR-039) can carry an out-of-union
  // targetType; loadEntities already degrades it to an empty list rather than throwing
  // (see entity-lister.ts). Fall back to the device copy so the picker renders instead
  // of crashing on `t(undefined.select)`.
  const kindKeys = KIND_KEYS[kind] ?? KIND_KEYS.device;

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
      // Only a complete current read can prove that the stored token is unknown.
      const named = Boolean(kindKeys.unknown && current !== undefined);
      opts.unshift({ value, label: named ? t(kindKeys.unknown!, { token: value }) : value, wrap: named });
    }
    return opts;
  }, [current, value, kindKeys, t]);

  return (
    <>
      {error && <p role="alert" className="text-sm text-destructive"><span>{t(kindKeys.failed)}</span> {error}</p>}
      <Combobox
        disabled={loading || !!error}
        id={id}
        ariaLabel={ariaLabel}
        options={options}
        value={value}
        onChange={onChange}
        placeholder={loading ? t('common:loading') : (placeholder ?? t(kindKeys.select))}
        searchPlaceholder={t(kindKeys.search)}
        emptyMessage={loading ? t('common:loading') : error ? t(kindKeys.failed) : t(kindKeys.empty)}
      />
    </>
  );
}
