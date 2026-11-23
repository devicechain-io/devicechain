// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ gql: vi.fn() }));
vi.mock('@devicechain/client', () => ({ gql: h.gql }));
import { approveAgentProposal, getAgentProposal, listAgentProposals, rejectAgentProposal } from './agent-proposals';

beforeEach(() => {
  h.gql.mockReset();
  h.gql.mockResolvedValue({ agentProposal: { token: 'p1' }, agentProposals: { results: [] },
    approveAgentProposal: { token: 'p1', state: 'EXECUTED', reasons: [] } });
});
it('routes command list and detail to the fixed command owner', async () => {
  await listAgentProposals('command-delivery', { pageNumber: 2, pageSize: 25, kind: 'COMMAND_SEND' });
  expect(h.gql.mock.calls[0][0]).toBe('command-delivery');
  expect(h.gql.mock.calls[0][2]).toEqual({ criteria: { pageNumber: 2, pageSize: 25, kind: 'COMMAND_SEND' } });
  await getAgentProposal('command-delivery', 'p1');
  expect(h.gql.mock.calls[1][0]).toBe('command-delivery');
  expect(String(h.gql.mock.calls[1][1])).toContain('resultRef');
  expect(h.gql.mock.calls[1][2]).toEqual({ token: 'p1' });
});
it('sends only the stored token and displayed digest on command approval', async () => {
  await approveAgentProposal('command-delivery', 'p1', 'displayed-digest');
  expect(h.gql).toHaveBeenCalledExactlyOnceWith('command-delivery', expect.anything(), { token: 'p1', digest: 'displayed-digest' });
  const document = String(h.gql.mock.calls[0][1]);
  expect(document).toContain('approveAgentProposal(token: $token, digest: $digest)');
  expect(document).not.toMatch(/payload|input|arguments/);
});
it('keeps the old alarm signatures and rejects an unknown owner', async () => {
  await getAgentProposal('p1');
  await approveAgentProposal('p1', 'd');
  await rejectAgentProposal('p1', 'note');
  expect(h.gql.mock.calls.map(call => call[0])).toEqual(['device-management', 'device-management', 'device-management']);
  await expect(getAgentProposal('https://other.invalid' as never, 'p1')).rejects.toThrow('Unknown proposal area');
  expect(h.gql).toHaveBeenCalledTimes(3);
});
it('rejects a command through its owner without resubmitting the action', async () => {
  await rejectAgentProposal('command-delivery', 'p1', 'keep running');
  expect(h.gql).toHaveBeenCalledExactlyOnceWith('command-delivery', expect.anything(), { token: 'p1', note: 'keep running' });
});
