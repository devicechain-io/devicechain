// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ComboboxOption } from '@/components/ui/combobox';

/** The slice of a tenant deletion record the audit tenant filter needs. */
export interface RetainedJournal {
  token: string;
  retainedAuditTenant: string;
  completedAt?: string | null;
}

/**
 * Options for the audit page's tenant filter: every live tenant under its token, then every
 * COMPLETED tenant deletion under the key its retained journal entries are filed under.
 *
 * A deleted tenant's entries are kept as evidence of the deletion but moved off its token (so
 * a new tenant created with the same token starts with an empty journal), which means the
 * live-tenant list can never reach them. Only a completed deletion has re-keyed anything, so
 * an in-flight one is not offered. The value sent as the filter is the retained key itself,
 * never the bare token; the label carries the token and the date so two deletions of one
 * token can be told apart.
 */
export function tenantFilterOptions(
  liveTokens: string[],
  deletions: RetainedJournal[],
  deletedLabel: (token: string, completedAt: string) => string,
): ComboboxOption[] {
  const live: ComboboxOption[] = liveTokens.map((value) => ({ value }));
  const deleted: ComboboxOption[] = deletions
    .filter((d) => d.completedAt && d.retainedAuditTenant)
    .map((d) => ({
      value: d.retainedAuditTenant,
      label: deletedLabel(d.token, d.completedAt as string),
    }));
  return [...live, ...deleted];
}
