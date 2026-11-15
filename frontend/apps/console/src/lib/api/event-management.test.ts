// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@devicechain/client')>()),
  gql: gqlMock,
}));

const opts = { deviceToken: 'dev-1', pageNumber: 2, pageSize: 10 };
beforeEach(() => {
  vi.resetModules();
  gqlMock.mockReset();
});

describe.each([
  ['listEvents', 'events', 'listPresenceTransitions', 'stateChangeEvents'],
  ['listPresenceTransitions', 'stateChangeEvents', 'listEvents', 'events'],
] as const)('%s', (method, field, otherMethod, otherField) => {
  it('requests no total and preserves the server hasMore', async () => {
    const api = await import('./event-management');
    const page = { results: [], pagination: { totalRecords: null, hasMore: true } };
    gqlMock.mockResolvedValue({ [field]: page });
    expect(await api[method](opts)).toEqual(page);
    expect(gqlMock.mock.calls[0][2]).toEqual({ criteria: { ...opts, includeTotal: false } });
    expect(String(gqlMock.mock.calls[0][1])).toMatch(/hasMore/);
    expect(String(gqlMock.mock.calls[0][1])).toMatch(/totalRecords/);
  });

  it.each([
    'Variable "criteria" has invalid value.\nField "includeTotal" is not defined by type "EventSearchCriteria".',
    'Cannot query field "hasMore" on type "SearchResultsPagination".',
  ])('remembers the legacy documents after %s', async (message) => {
    const api = await import('./event-management');
    const page = { results: [], pagination: { pageEnd: 20, totalRecords: 21 } };
    gqlMock.mockRejectedValueOnce(new GraphQLRequestError(message, 200, [{ message }]))
      .mockResolvedValueOnce({ [field]: page })
      .mockResolvedValueOnce({ [field]: { ...page, pagination: { pageEnd: 21, totalRecords: 21 } } })
      .mockResolvedValueOnce({ [otherField]: page });
    expect((await api[method](opts)).pagination.hasMore).toBe(true);
    expect((await api[method](opts)).pagination.hasMore).toBe(false);
    expect((await api[otherMethod](opts)).pagination.hasMore).toBe(true);
    expect(gqlMock).toHaveBeenCalledTimes(4);
    for (const call of gqlMock.mock.calls.slice(1)) {
      expect(call[2]).toEqual({ criteria: opts });
      expect(String(call[1])).not.toMatch(/hasMore|includeTotal/);
      expect(String(call[1])).toMatch(/pageEnd/);
      expect(String(call[1])).toMatch(/totalRecords/);
    }
  });

  it('also checks GraphQL messages after the first one', async () => {
    const api = await import('./event-management');
    gqlMock.mockRejectedValueOnce(new GraphQLRequestError('Validation failed', 200, [
      { message: 'Validation failed' }, { message: 'Cannot query field "hasMore" on type "SearchResultsPagination".' },
    ])).mockResolvedValueOnce({ [field]: { results: [], pagination: { pageEnd: 0, totalRecords: 0 } } });
    expect((await api[method](opts)).pagination.hasMore).toBe(false);
    expect(gqlMock).toHaveBeenCalledTimes(2);
  });

  it.each([
    new GraphQLRequestError('Forbidden', 200, [{ message: 'Forbidden' }]),
    // Errors that merely NAME a new field are not the refusal of it.
    new GraphQLRequestError('includeTotal', 200, [{ message: 'reading includeTotal failed: permission denied' }]),
    new GraphQLRequestError('hasMore', 200, [{ message: 'resolver hasMore: timeout' }]),
    new GraphQLRequestError('hasMore network failure', 0),
    new Error('Unrelated failure'),
  ])('propagates unrelated errors without retrying', async (error) => {
    const api = await import('./event-management');
    gqlMock.mockRejectedValue(error);
    await expect(api[method](opts)).rejects.toBe(error);
    expect(gqlMock).toHaveBeenCalledTimes(1);
  });

  it('does not retry a failed legacy request', async () => {
    const api = await import('./event-management');
    const message = 'Variable "criteria" has invalid value.\nField "includeTotal" is not defined by type "EventSearchCriteria".';
    const error = new GraphQLRequestError(message, 200, [{ message }]);
    gqlMock.mockRejectedValue(error);
    await expect(api[method](opts)).rejects.toBe(error);
    expect(gqlMock).toHaveBeenCalledTimes(2);
  });
});
