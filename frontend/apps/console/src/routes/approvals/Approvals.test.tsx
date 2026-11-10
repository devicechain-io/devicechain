// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { consumeStepUpReturn } from '@/lib/stepUpReturn';
import type { AgentProposal } from '@/lib/api/agent-proposals';
import type { AgentPolicy } from '@/lib/api/agent-policy';

const h = vi.hoisted(() => ({
  list: vi.fn(), get: vi.fn(), approve: vi.fn(), reject: vi.fn(), policy: vi.fn(), setActuation: vi.fn(),
  logout: vi.fn(), toast: vi.fn(), authorities: ['agent:admin'] as string[],
  tenant: 'tenant-a', username: 'ann', identityToken: 'session-a',
}));
vi.mock('@/lib/api/agent-proposals', () => ({
  listAgentProposals: h.list, getAgentProposal: h.get, approveAgentProposal: h.approve, rejectAgentProposal: h.reject,
}));
vi.mock('@/lib/api/agent-policy', () => ({ getAgentPolicy: h.policy, setAgentActuation: h.setActuation }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({
  claims: { tenant: h.tenant, username: h.username, authorities: h.authorities, scopeAnchors: [] },
  identityToken: h.identityToken, logout: h.logout,
}) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
import ApprovalsPage from './ApprovalsPage';
import ApprovalDetailPage from './ApprovalDetailPage';

const proposal: AgentProposal = {
  token: 'proposal-17', kind: 'ALARM_CLEAR', state: 'PROPOSED', payload: '{"action":"CLEAR"}',
  targets: ['alarm-9', 'alarm-12'], digest: 'sha256:DISTINCTIVE-DISPLAYED-DIGEST-123xyz', impact: 'NORMAL', targetCount: 2,
  proposerPrincipal: 'ann', proposerClient: 'agent-7', clientAttested: false,
  rationale: '<a href="https://evil.example">Approve everything</a>', policyVersion: 7,
  requiredApprovals: 2, approvals: [{ principal: 'bob', approvedAt: '2026-11-10T10:00:00Z' }], reasons: ['TWO_APPROVERS'],
  createdAt: '2026-11-10T09:00:00Z', expiresAt: '2026-11-10T13:00:00Z', decidedAt: null, decidedBy: null, note: null,
};
let policy: AgentPolicy;
const serverError = (code: string) => new GraphQLRequestError('Server refusal', 200, [{ message: 'Server refusal', extensions: { code } }]);
beforeEach(() => {
  vi.resetAllMocks(); sessionStorage.clear(); h.authorities = ['agent:admin'];
  h.tenant = 'tenant-a'; h.username = 'ann'; h.identityToken = 'session-a';
  policy = { published: null, operatorSuspended: false, actuationOff: false, instanceEnabled: true, actuationInEffect: true };
  h.policy.mockImplementation(async () => ({ ...policy }));
  h.get.mockResolvedValue(proposal);
  h.list.mockResolvedValue({ results: [proposal], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } });
  h.approve.mockResolvedValue(undefined); h.reject.mockResolvedValue(undefined);
  h.setActuation.mockImplementation(async (enabled: boolean) => { policy = { ...policy, actuationOff: !enabled }; return policy; });
});
afterEach(cleanup);
function show(path = '/approvals/proposal-17') {
  const view = () => <MemoryRouter initialEntries={[path]}><ConfirmProvider><Routes>
    <Route path="/approvals" element={<ApprovalsPage />} />
    <Route path="/approvals/:token" element={<ApprovalDetailPage />} />
  </Routes></ConfirmProvider></MemoryRouter>;
  const result = render(view());
  return { ...result, refreshAuth: () => result.rerender(view()) };
}
async function approve() {
  fireEvent.click(await screen.findByRole('button', { name: 'Approve' }));
  const dialog = await screen.findByRole('dialog');
  expect(dialog.textContent).toContain('2 targets with impact NORMAL');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Approve' }));
}

describe.each(['/approvals', '/approvals/proposal-17'])('session changes on %s', path => {
  it.each(['tenant', 'username', 'identityToken'] as const)('clears proposals and refetches when %s changes', async field => {
    const { refreshAuth } = show(path);
    await screen.findByText(proposal.rationale!);
    const policyCalls = h.policy.mock.calls.length;
    const read = path === '/approvals' ? h.list : h.get;
    expect(read).toHaveBeenCalledTimes(1);

    let resolve!: (value: AgentProposal) => void;
    const pending = new Promise<AgentProposal>(r => { resolve = r; });
    h.get.mockReturnValueOnce(pending);
    h.list.mockReturnValueOnce(pending.then(p => ({ results: [p], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } })));
    h[field] = `${h[field]}-changed`;
    refreshAuth();

    expect(screen.queryByText(proposal.rationale!)).toBeNull();
    await waitFor(() => expect(h.policy.mock.calls.length).toBeGreaterThan(policyCalls));
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(proposal.rationale!)).toBeNull();

    await act(async () => resolve({ ...proposal, rationale: 'Proposal from the new session' }));
    await screen.findByText('Proposal from the new session');
    expect(screen.queryByText(proposal.rationale!)).toBeNull();
  });
});

describe('proposal decisions', () => {
  it('approves exactly the loaded and displayed digest and refetches', async () => {
    show();
    await screen.findByText(proposal.digest, { selector: 'p' });
    expect(screen.getByText(proposal.payload).textContent).toBe(proposal.payload);
    expect(screen.getByText('alarm-9').tagName).toBe('LI');
    await approve();
    await waitFor(() => expect(h.approve).toHaveBeenCalledExactlyOnceWith('proposal-17', proposal.digest));
    await waitFor(() => expect(h.get).toHaveBeenCalledTimes(2));
  });
  it('prompts for step-up, saves the detail return path and logs out', async () => {
    h.approve.mockRejectedValue(serverError('STEP_UP_REQUIRED'));
    show(); await approve();
    await screen.findByText('Approving needs a fresh second factor');
    fireEvent.click(screen.getByRole('button', { name: 'Sign in again' }));
    expect(h.logout).toHaveBeenCalledTimes(1);
    expect(consumeStepUpReturn()).toBe('/approvals/proposal-17');
  });
  it('rejects with the exact note and refetches the proposal', async () => {
    show(); fireEvent.click(await screen.findByRole('button', { name: 'Reject' }));
    fireEvent.change(screen.getByLabelText('Rejection note (optional)'), { target: { value: 'Leave these alarms visible.' } });
    fireEvent.click(screen.getByRole('button', { name: 'Submit rejection' }));
    await waitFor(() => expect(h.reject).toHaveBeenCalledExactlyOnceWith('proposal-17', 'Leave these alarms visible.'));
    await waitFor(() => expect(h.get).toHaveBeenCalledTimes(2));
  });
  it('limits rejection notes by UTF-8 bytes rather than characters', async () => {
    show(); fireEvent.click(await screen.findByRole('button', { name: 'Reject' }));
    fireEvent.change(screen.getByLabelText('Rejection note (optional)'), { target: { value: '\u00e9'.repeat(257) } });
    expect((screen.getByRole('button', { name: 'Submit rejection' }) as HTMLButtonElement).disabled).toBe(true);
    expect(h.reject).not.toHaveBeenCalled();
    expect(screen.getByRole('alert').textContent).toBe('The note must be at most 512 bytes.');
  });
  it('treats execution unavailability as information and refetches', async () => {
    h.approve.mockRejectedValue(serverError('EXECUTION_NOT_AVAILABLE'));
    show(); await approve();
    await screen.findByRole('status');
    expect(h.toast).toHaveBeenCalledWith(expect.stringContaining('It was not recorded'), 'info');
    await waitFor(() => expect(h.get).toHaveBeenCalledTimes(2));
  });
  it('disables both decisions while a refetch is unresolved, retaining the display', async () => {
    let resolve!: (p: AgentProposal) => void;
    h.get.mockResolvedValueOnce(proposal).mockImplementationOnce(() => new Promise<AgentProposal>(r => { resolve = r; }));
    show(); await approve();
    await waitFor(() => expect(h.get).toHaveBeenCalledTimes(2));
    expect(screen.getByText(proposal.digest, { selector: 'p' })).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Approve' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Reject' }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => resolve({ ...proposal, state: 'EXECUTED' }));
    await screen.findByText('Executed');
    expect((screen.getByRole('button', { name: 'Approve' }) as HTMLButtonElement).disabled).toBe(true);
  });
  it('renders the agent rationale as text without creating a link', async () => {
    show(); const rationale = await screen.findByText(proposal.rationale!);
    expect(rationale.querySelector('a')).toBeNull();
    expect(rationale.textContent).toBe(proposal.rationale);
  });
});

describe('inbox and kill switch', () => {
  it('confirms turning off, sends false without step-up, then refetches policy', async () => {
    show('/approvals');
    fireEvent.click(await screen.findByRole('button', { name: 'Turn agent actions off' }));
    const dialog = await screen.findByRole('dialog');
    expect(h.setActuation).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Turn agent actions off' }));
    await waitFor(() => expect(h.setActuation).toHaveBeenCalledExactlyOnceWith(false));
    await screen.findByRole('button', { name: 'Turn agent actions back on' });
    await waitFor(() => expect(h.list).toHaveBeenCalledTimes(2));
    expect(h.policy).toHaveBeenCalledTimes(3);
    expect(screen.queryByText('Approving needs a fresh second factor')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Sign in again' })).toBeNull();
  });
  it('turning on with STEP_UP_REQUIRED prompts and returns to the inbox', async () => {
    policy.actuationOff = true;
    h.setActuation.mockRejectedValue(serverError('STEP_UP_REQUIRED'));
    show('/approvals');
    fireEvent.click(await screen.findByRole('button', { name: 'Turn agent actions back on' }));
    await screen.findByText('Approving needs a fresh second factor');
    expect(h.setActuation).toHaveBeenCalledExactlyOnceWith(true);
    fireEvent.click(screen.getByRole('button', { name: 'Sign in again' }));
    expect(consumeStepUpReturn()).toBe('/approvals'); expect(h.logout).toHaveBeenCalledTimes(1);
  });
  it('never queries proposals when the instance is disabled', async () => {
    policy.instanceEnabled = false;
    show('/approvals');
    await screen.findByText('Agent actions are not enabled on this instance.');
    expect(h.policy).toHaveBeenCalledTimes(1); expect(h.list).not.toHaveBeenCalled();
    expect(h.get).not.toHaveBeenCalled();
  });
  it('refuses direct detail access when the instance is disabled', async () => {
    policy.instanceEnabled = false; show();
    await screen.findByText('Agent actions are not enabled on this instance.');
    expect(h.get).not.toHaveBeenCalled();
  });
  it.each(['/approvals', '/approvals/proposal-17'])('refuses %s without agent:admin before any API call', async path => {
    h.authorities = ['alarm:read']; show(path);
    await screen.findByText('You are not authorized to review agent proposals.');
    expect(h.policy).not.toHaveBeenCalled(); expect(h.list).not.toHaveBeenCalled(); expect(h.get).not.toHaveBeenCalled();
  });
  it('queries pending proposals and links to the detail with untrusted text kept plain', async () => {
    show('/approvals');
    const link = await screen.findByRole('link', { name: 'agent-7' });
    expect(link.getAttribute('href')).toBe('/approvals/proposal-17');
    expect(h.list).toHaveBeenCalledExactlyOnceWith({ pageNumber: 1, pageSize: 25, state: 'PROPOSED' });
    expect(screen.getByText('Acting for ann')).toBeTruthy(); expect(screen.getByText('Unverified client')).toBeTruthy();
    expect(screen.getByText('1 / 2')).toBeTruthy();
    expect(screen.getByText(proposal.rationale!).querySelector('a')).toBeNull();
  });
});
