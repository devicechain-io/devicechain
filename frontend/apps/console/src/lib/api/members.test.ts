// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// listAllMembers reads a filter's members page by page up to a cap, and says whether it
// stopped at the cap. The flag is the only thing that tells a view its list is not the
// whole answer, so the boundary is pinned on both sides: exactly the cap is complete, one
// past it is not.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: gqlMock,
}));

const { customerAnchor, listAllMembers } = await import('./members');

// A fake members query over `total` members; totalRecords is reported unless `hideTotal`.
function serve(total: number, hideTotal = false) {
  gqlMock.mockImplementation(async (_area: string, _doc: string, vars: { criteria: { pageNumber: number; pageSize: number } }) => {
    const { pageNumber, pageSize } = vars.criteria;
    const start = (pageNumber - 1) * pageSize;
    const n = Math.max(0, Math.min(pageSize, total - start));
    return {
      members: {
        results: Array.from({ length: n }, (_, i) => ({ username: `u${start + i}@example.com` })),
        pagination: { pageStart: start, pageEnd: start + n, totalRecords: hideTotal ? null : total },
      },
    };
  });
}

// A block body: mockReset returns the mock, and a function returned from beforeEach is run
// as a cleanup hook.
beforeEach(() => {
  gqlMock.mockReset();
});

describe('listAllMembers', () => {
  it('reads every page of a filter that fits, and says it is complete', async () => {
    serve(2500);
    const got = await listAllMembers({ scopeAnchor: customerAnchor('acme') });
    expect(got.truncated).toBe(false);
    expect(got.members).toHaveLength(2500);
    expect(gqlMock).toHaveBeenCalledTimes(3);
    for (const c of gqlMock.mock.calls) {
      expect(c[2].criteria.scopeAnchor).toEqual(customerAnchor('acme'));
    }
  });

  it('is complete at exactly the cap', async () => {
    serve(10_000);
    const got = await listAllMembers();
    expect(got.truncated).toBe(false);
    expect(got.members).toHaveLength(10_000);
  });

  it('stops at the cap and says so when more matched, rather than throwing or reading on', async () => {
    serve(10_001);
    const got = await listAllMembers();
    expect(got.truncated).toBe(true);
    expect(got.members).toHaveLength(10_000);
    expect(gqlMock).toHaveBeenCalledTimes(10);
  });

  it('says so at the cap when the server reports no total', async () => {
    serve(20_000, true);
    const got = await listAllMembers();
    expect(got.truncated).toBe(true);
    expect(got.members).toHaveLength(10_000);
  });
});
