// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Server-side search loaders for AsyncCombobox: each asks for one small page of matches
// instead of reading a whole registry (whose tail past 1,000 rows no picker could reach).
import type { ComboboxOption } from '@/components/ui/combobox';
import { ASYNC_COMBOBOX_PAGE_SIZE, type AsyncComboboxPage } from '@/components/ui/async-combobox';
import { getDeviceType, listDeviceTypes, listDevices } from '@/lib/api/device-management';

type Named = { token: string; name?: string | null };

export function optionOf(item: Named): ComboboxOption {
  return {
    value: item.token,
    label: item.name || item.token,
    description: item.name ? item.token : undefined,
  };
}

export async function searchDeviceTypes(query: string): Promise<AsyncComboboxPage> {
  const page = await listDeviceTypes({ pageNumber: 1, pageSize: ASYNC_COMBOBOX_PAGE_SIZE, nameContains: query });
  return { options: page.results.map(optionOf), total: page.pagination.totalRecords ?? undefined };
}

export async function resolveDeviceType(token: string): Promise<ComboboxOption | null> {
  const found = await getDeviceType(token);
  return found ? optionOf(found) : null;
}

export async function searchDevices(query: string): Promise<AsyncComboboxPage> {
  const page = await listDevices({ pageNumber: 1, pageSize: ASYNC_COMBOBOX_PAGE_SIZE, nameContains: query });
  return { options: page.results.map(optionOf), total: page.pagination.totalRecords ?? undefined };
}
