// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from 'vitest';

const gql = vi.fn();
vi.mock('@devicechain/client', () => ({ gql: (...a: unknown[]) => gql(...a) }));

import { ACKNOWLEDGE_BATCH_LIMIT, acknowledgeAlarms, listAlarms } from './alarms';

const tokens = (n: number) => Array.from({ length: n }, (_, i) => `a-${i}`);

beforeEach(() => {
  gql.mockReset();
  gql.mockImplementation(async (_service: string, _doc: unknown, vars: { tokens: string[] }) => ({
    acknowledgeAlarms: { acknowledged: vars.tokens.map((token) => ({ token })), refusals: [] },
  }));
});

describe('acknowledgeAlarms', () => {
  it('sends nothing for an empty selection', async () => {
    expect(await acknowledgeAlarms([])).toEqual({ acknowledged: 0, refusals: [] });
    expect(gql).not.toHaveBeenCalled();
  });

  it('names each alarm once, however often the caller listed it', async () => {
    const out = await acknowledgeAlarms(['x', 'y', 'x']);
    expect(gql).toHaveBeenCalledTimes(1);
    expect(gql.mock.calls[0][2]).toEqual({ tokens: ['x', 'y'] });
    expect(out.acknowledged).toBe(2);
  });

  it('splits a selection past the per-request limit and adds the outcomes up', async () => {
    const out = await acknowledgeAlarms(tokens(ACKNOWLEDGE_BATCH_LIMIT + 1));
    expect(gql).toHaveBeenCalledTimes(2);
    expect(gql.mock.calls[0][2].tokens).toHaveLength(ACKNOWLEDGE_BATCH_LIMIT);
    expect(gql.mock.calls[1][2].tokens).toEqual([`a-${ACKNOWLEDGE_BATCH_LIMIT}`]);
    expect(out.acknowledged).toBe(ACKNOWLEDGE_BATCH_LIMIT + 1);
  });

  it('reports the tokens the server refused, with its reason', async () => {
    gql.mockResolvedValueOnce({
      acknowledgeAlarms: {
        acknowledged: [{ token: 'a' }],
        refusals: [{ token: 'ghost', code: 'ALARM_NOT_FOUND', reason: 'no such alarm' }],
      },
    });
    expect(await acknowledgeAlarms(['a', 'ghost'])).toEqual({
      acknowledged: 1,
      refusals: [{ token: 'ghost', reason: 'no such alarm' }],
    });
  });

  it('stops at a failed request and lets the error through', async () => {
    gql.mockResolvedValueOnce({ acknowledgeAlarms: { acknowledged: [], refusals: [] } });
    gql.mockRejectedValueOnce(new Error('forbidden'));
    await expect(acknowledgeAlarms(tokens(ACKNOWLEDGE_BATCH_LIMIT * 3))).rejects.toThrow('forbidden');
    expect(gql).toHaveBeenCalledTimes(2);
  });
});

describe('listAlarms', () => {
  beforeEach(() => gql.mockResolvedValue({ alarms: { results: [], pagination: {} } }));

  it('narrows to one originator only when a token is given', async () => {
    await listAlarms({ pageNumber: 1, pageSize: 10 });
    expect(gql.mock.calls[0][2].criteria).not.toHaveProperty('originatorToken');
    await listAlarms({ pageNumber: 1, pageSize: 10, originatorType: 'device', originatorToken: 'pump-1' });
    expect(gql.mock.calls[1][2].criteria).toMatchObject({ originatorType: 'device', originatorToken: 'pump-1' });
  });
});
