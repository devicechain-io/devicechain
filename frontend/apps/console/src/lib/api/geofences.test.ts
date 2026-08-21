// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The frozen-set reader's loop.
//
// 🔴 THIS FILE EXISTS BECAUSE TWO READERS OF ONE FIELD MUST NOT DISAGREE ABOUT WHAT A
// NULL MEANS. `fences` is paginated and its pagination record's fields are nullable Ints.
// The Go reader in event-processing refuses a page that reports no totalRecords, because
// treating it as zero ends the walk after one page and a truncated fence set is
// indistinguishable downstream from a small one. This reader used to write `?? 0` and do
// exactly that — quietly, inside a render path, drawing a confident picture of a set that
// is not the set.
//
// The assertions here are therefore mostly about what does NOT happen: no short answer, no
// unbounded loop.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', () => ({ gql: gqlMock }));

const { getGeoFenceSetSnapshot } = await import('./geofences');

/** One page of the frozen-set answer. */
function page(
  version: number,
  fences: { token: string; geometry: string }[],
  totalRecords: number | null,
) {
  return {
    geoFenceSetSnapshot: {
      version,
      fences: { results: fences, pagination: { totalRecords } },
    },
  };
}

const FENCE = (token: string) => ({ token, geometry: '{"kind":"POLYGON_2D"}' });

beforeEach(() => {
  gqlMock.mockReset();
});

describe('getGeoFenceSetSnapshot', () => {
  it('returns the whole set when one page holds it', async () => {
    gqlMock.mockResolvedValue(page(7, [FENCE('a'), FENCE('b')], 2));

    await expect(getGeoFenceSetSnapshot(7)).resolves.toEqual({
      version: 7,
      fences: [FENCE('a'), FENCE('b')],
    });
    expect(gqlMock).toHaveBeenCalledTimes(1);
  });

  it('reads every page of a set that does not fit in one', async () => {
    gqlMock
      .mockResolvedValueOnce(page(7, [FENCE('a')], 2))
      .mockResolvedValueOnce(page(7, [FENCE('b')], 2));

    const snapshot = await getGeoFenceSetSnapshot(7);

    expect(snapshot.fences.map((f) => f.token)).toEqual(['a', 'b']);
    expect(gqlMock).toHaveBeenCalledTimes(2);
    // The second request must ask for the SECOND page — asking for page one twice would
    // produce the right count and the wrong set.
    const asked = gqlMock.mock.calls.map(([, , v]) => (v as { pagination: { pageNumber: number } }).pagination.pageNumber);
    expect(asked).toEqual([1, 2]);
  });

  // 🔴 THE ONE THAT MATTERS. A missing total must not become a zero.
  it('refuses a page that reports no record count rather than answering short', async () => {
    gqlMock.mockResolvedValue(page(7, [FENCE('a')], null));

    await expect(getGeoFenceSetSnapshot(7)).rejects.toThrow(/record count/i);
  });

  it('refuses a set that stops answering before its total', async () => {
    gqlMock
      .mockResolvedValueOnce(page(7, [FENCE('a')], 5))
      .mockResolvedValueOnce(page(7, [], 5));

    await expect(getGeoFenceSetSnapshot(7)).rejects.toThrow(/stopped answering/i);
  });

  // It runs inside a render path, so a server that never completes the set must produce a
  // rejected promise rather than a hung tab.
  it('gives up rather than looping forever on a total it never reaches', async () => {
    gqlMock.mockResolvedValue(page(7, [FENCE('a')], 1_000_000));

    await expect(getGeoFenceSetSnapshot(7)).rejects.toThrow(/did not finish loading/i);
    // Bounded, and bounded low enough to be a runaway stop rather than a slow failure.
    expect(gqlMock.mock.calls.length).toBeLessThanOrEqual(8);
  });

  // The version reported by the SERVER is what comes back, not the one that was asked for.
  // The panel renders it next to the fences, and rendering the requested number over a
  // different set is the confusion the version stamp exists to prevent.
  it('reports the version the server answered with', async () => {
    gqlMock.mockResolvedValue(page(4, [FENCE('a')], 1));

    await expect(getGeoFenceSetSnapshot(9)).resolves.toMatchObject({ version: 4 });
  });
});
