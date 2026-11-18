// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { GraphQLRequestError } from '@devicechain/client';

export const PICKER_PAGE_SIZE = 200;
export const MAX_PICKER_OPTIONS = 10_000;

interface Page<T> {
  results: T[];
  pagination: { totalRecords: number | null };
}

/** Materialize a complete picker, or reject; a partial list is never selectable.
 * This is a browser rendering bound, not a limit on the served GraphQL API.
 * Offset paging cannot provide a snapshot, so detect changing totals and repeated
 * identities and ask for a retry rather than presenting known-incomplete options.
 */
export async function listAll<T extends { token: string }>(
  load: (page: { pageNumber: number; pageSize: number }) => Promise<Page<T>>,
): Promise<{ results: T[]; pagination: { pageStart: number; pageEnd: number; totalRecords: number } }> {
  const results: T[] = [];
  const seen = new Set<string>();
  let expected: number | undefined;
  for (let pageNumber = 1; ; pageNumber++) {
    const page = await load({ pageNumber, pageSize: PICKER_PAGE_SIZE });
    const total = page.pagination?.totalRecords;
    const fail = (message: string): never => { throw new GraphQLRequestError(message, 0); };
    if (total == null || !Number.isSafeInteger(total) || total < 0) {
      throw new GraphQLRequestError('Cannot load complete options: totalRecords is missing or invalid.', 0);
    }
    const count = total;
    if (count > MAX_PICKER_OPTIONS) {
      fail(`Picker limit MAX_PICKER_OPTIONS (${MAX_PICKER_OPTIONS}) exceeded: ${count} records. Narrow the search before selecting.`);
    }
    if (expected !== undefined && count !== expected) {
      fail('Options changed while loading. Reload and try again.');
    }
    expected = count;
    for (const row of page.results) {
      if (seen.has(row.token)) fail('Options repeated while paging. Reload and try again.');
      seen.add(row.token);
      results.push(row);
    }
    if (results.length > count || page.results.length > PICKER_PAGE_SIZE) {
      fail('Cannot load complete options: inconsistent pagination.');
    }
    if (results.length === count) {
      return { results, pagination: { pageStart: count ? 1 : 0, pageEnd: count, totalRecords: count } };
    }
    if (page.results.length !== PICKER_PAGE_SIZE) {
      fail('Cannot load complete options: a page ended before totalRecords.');
    }
  }
}
