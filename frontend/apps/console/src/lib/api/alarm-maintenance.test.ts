// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import { beforeEach, describe, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ gql: vi.fn() }));
vi.mock('@devicechain/client', async original => ({ ...await original<typeof import('@devicechain/client')>(), gql: h.gql }));
import { GraphQLRequestError } from '@devicechain/client';
import { suppressionStatus, suppressionHistoryMarks, listWindows, deviceWindows, getWindow, listHistoryAlarms } from './alarm-maintenance';
beforeEach(() => { h.gql.mockReset(); });
describe('suppression transport', () => {
  it('deduplicates and chunks history marks, preserving the cycle stamp', async () => {
    h.gql.mockImplementation((_service, _doc, { tokens }) => Promise.resolve({ alarmsByToken: tokens.map((token: string) => ({ token, raisedDuringSuppression: 'restricted' })) }));
    const tokens = Array.from({ length: 451 }, (_, i) => `a-${i}`);
    const result = await suppressionHistoryMarks([...tokens, tokens[0]]);
    expect(h.gql.mock.calls.map(call => call[2].tokens.length)).toEqual([200, 200, 51]);
    expect(String(h.gql.mock.calls[0][1])).toContain('query AlarmMaintenanceHistoryMarks');
    expect(result).toEqual(tokens.map(token => ({ token, raisedDuringSuppression: 'restricted' })));
  });
  it('omits history marks when Alarm lacks the optional field', async () => {
    const message = 'Cannot query field "raisedDuringSuppression" on type "Alarm".';
    h.gql.mockRejectedValue(new GraphQLRequestError(message, 200, [{ message }]));
    expect(await suppressionHistoryMarks(['a'])).toBeNull();
  });
  it('batches at 200, deduplicates, and merges status by alarm token', async () => {
    h.gql.mockImplementation((_service, _doc, { tokens }) => Promise.resolve({ alarmSuppressionStatus: tokens.map((alarmToken: string) => ({ alarmToken, suppression: null })) }));
    const tokens = Array.from({ length: 451 }, (_, i) => `a-${i}`);
    const result = await suppressionStatus([...tokens, tokens[0]]);
    expect(h.gql.mock.calls.map(call => call[2].tokens.length)).toEqual([200, 200, 51]);
    expect(result?.map(row => row.alarmToken)).toEqual(tokens);
  });
  it('does not query for an empty list', async () => {
    expect(await suppressionStatus([])).toEqual([]);
    expect(await suppressionHistoryMarks([])).toEqual([]);
    expect(h.gql).not.toHaveBeenCalled();
  });
  it.each(['status', 'list', 'device', 'get', 'history'])('omits the optional %s response on validation refusal', async method => {
    const message = 'Cannot query field "alarmSuppressionStatus" on type "Query".';
    h.gql.mockRejectedValue(new GraphQLRequestError(message, 200, [{ message }]));
    const result = method === 'status' ? suppressionStatus(['a']) : method === 'list' ? listWindows({ pageNumber: 1, pageSize: 1 }) : method === 'device' ? deviceWindows('pump') : method === 'get' ? getWindow('w') : listHistoryAlarms({ pageNumber: 1, pageSize: 25 }, true);
    expect(await result).toBeNull();
  });
  it('sends history criteria only through the opt-in document', async () => {
    h.gql.mockResolvedValue({ alarms: { results: [], pagination: {} } });
    await listHistoryAlarms({ pageNumber: 1, pageSize: 25 }, true);
    expect(String(h.gql.mock.calls[0][1])).toContain('raisedDuringSuppression: $raisedDuringSuppression');
    expect(h.gql.mock.calls[0][2]).toEqual({ pageNumber: 1, pageSize: 25, raisedDuringSuppression: true });
  });
});
