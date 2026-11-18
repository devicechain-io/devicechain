// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from 'vitest';

const gql = vi.fn();
vi.mock('@devicechain/client', () => ({ gql: (...args: unknown[]) => gql(...args) }));

import { listAutomationReviews, matchingDraftReviewTokens } from './automation-reviews';

beforeEach(() => { gql.mockReset(); });

describe('review list draft matching', () => {
  it('omits per-row digest resolution from the list document', async () => {
    gql.mockResolvedValue({ automationReviews: { results: [], pagination: { totalRecords: 0 } } });
    await listAutomationReviews({ profileToken: 'profile', pageNumber: 1, pageSize: 25 });
    expect(String(gql.mock.calls[0][1])).not.toContain('matchesCurrentDraft');
  });

  it('collects matching tokens across filtered pages', async () => {
    gql.mockResolvedValueOnce({ automationReviews: { results: [{ token: 'first' }], pagination: { totalRecords: 101 } } });
    gql.mockResolvedValueOnce({ automationReviews: { results: [{ token: 'last' }], pagination: { totalRecords: 101 } } });
    expect(await matchingDraftReviewTokens('profile')).toEqual(new Set(['first', 'last']));
    expect(gql).toHaveBeenCalledTimes(2);
    expect(gql.mock.calls.map(call => call[2])).toEqual([1, 2].map(pageNumber => ({ criteria: {
      profileToken: 'profile', matchingCurrentDraft: true, pageNumber, pageSize: 100,
    } })));
  });
});
