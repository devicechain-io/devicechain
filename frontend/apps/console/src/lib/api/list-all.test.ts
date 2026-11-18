// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';
import { listAll, MAX_PICKER_OPTIONS, PICKER_PAGE_SIZE } from './list-all';

const rows = (start: number, count: number) => Array.from({ length: count }, (_, i) => ({ token: String(start + i) }));
const page = (results: { token: string }[], totalRecords: number | null) => ({ results, pagination: { totalRecords } });

describe('complete picker options', () => {
  it('loads past both former cutoffs and keeps the final option', async () => {
    const all = rows(0, 1201);
    const load = vi.fn(async ({ pageNumber, pageSize }: { pageNumber: number; pageSize: number }) =>
      page(all.slice((pageNumber - 1) * pageSize, pageNumber * pageSize), all.length));
    expect((await listAll(load)).results).toEqual(all);
    expect(load).toHaveBeenCalledTimes(7);
    expect(load).toHaveBeenLastCalledWith({ pageNumber: 7, pageSize: PICKER_PAGE_SIZE });
  });

  it('accepts an empty list and the exact option limit', async () => {
    expect((await listAll(async () => page([], 0))).results).toEqual([]);
    const all = rows(0, MAX_PICKER_OPTIONS);
    const result = await listAll(async ({ pageNumber, pageSize }) =>
      page(all.slice((pageNumber - 1) * pageSize, pageNumber * pageSize), all.length));
    expect(result.results).toHaveLength(MAX_PICKER_OPTIONS);
  });

  it('names the limit and never exposes a partial list when over it', async () => {
    const load = vi.fn(async () => page(rows(0, 200), MAX_PICKER_OPTIONS + 1));
    await expect(listAll(load)).rejects.toThrow(`MAX_PICKER_OPTIONS (${MAX_PICKER_OPTIONS})`);
    expect(load).toHaveBeenCalledTimes(1);
  });

  it.each([null, -1, NaN, 1.5])('rejects an unknown or invalid total %s', async total => {
    await expect(listAll(async () => page([], total))).rejects.toThrow('totalRecords');
  });

  it('rejects a short page, rather than silently ending before the total', async () => {
    await expect(listAll(async () => page(rows(0, 10), 201))).rejects.toThrow('before totalRecords');
  });

  it('rejects changing totals and repeated pages', async () => {
    await expect(listAll(async ({ pageNumber }) =>
      page(rows((pageNumber - 1) * 200, 200), pageNumber === 1 ? 400 : 401)))
      .rejects.toThrow('changed');
    await expect(listAll(async () => page(rows(0, 200), 400))).rejects.toThrow('repeated');
  });

  it('propagates a later-page refusal without returning earlier rows', async () => {
    await expect(listAll(async ({ pageNumber }) => {
      if (pageNumber === 2) throw new Error('permission refused');
      return page(rows(0, 200), 201);
    })).rejects.toThrow('permission refused');
  });
});
