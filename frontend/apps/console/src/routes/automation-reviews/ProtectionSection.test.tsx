// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { GraphQLRequestError } from '@devicechain/client';
const h = vi.hoisted(() => ({ policy: vi.fn(), records: vi.fn(), set: vi.fn(), approve: vi.fn(), cancel: vi.fn(), username: 'ann', scoped: false }));
vi.mock('@/lib/api/automation-reviews', () => ({ getAutomationReviewPolicy: h.policy, listAutomationReviewBreakGlass: h.records,
  setAutomationReviewPolicy: h.set, approveAutomationReviewPolicyChange: h.approve, cancelAutomationReviewPolicyChange: h.cancel }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { username: h.username, authorities: ['device:write'], scopeAnchors: h.scoped ? ['customer'] : [] } }) }));
import { ProtectionSection } from './ProtectionSection';
const settings = { required: false, requiredApprovals: 1, allowSelfApproval: false, approvalTtlSeconds: 86400, stepUpMaxAgeSeconds: 900 };
const policy = { profileToken: 'profile', effective: settings, revision: 0, updatedBy: null, updatedAt: null, pendingChange: null };
const pending = { ...policy, pendingChange: { token: 'change', proposed: settings, proposedBy: 'bob', proposedAt: '2026-11-10T10:00:00Z', effectiveAt: null, state: 'PENDING', decidedBy: null, decidedAt: null } };
// jsdom has no ResizeObserver; Radix's checkbox measures its form-bubbling input with one.
beforeEach(() => { vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} }); });
beforeEach(() => { vi.resetAllMocks(); h.username = 'ann'; h.scoped = false; h.policy.mockResolvedValue(policy); h.records.mockResolvedValue([]); });
afterEach(cleanup);
function show() { render(<MemoryRouter><ProtectionSection profileToken="profile" /></MemoryRouter>); }
async function edit() { fireEvent.click(await screen.findByRole('button', { name: 'Edit protection' })); }
describe('publish protection', () => {
  it('shows the effective settings and applies a tightening', async () => {
    h.set.mockResolvedValue({ applied: true, policy: { ...policy, effective: { ...settings, required: true } } });
    show(); await edit(); expect(screen.getAllByText('Not protected').length).toBeGreaterThan(0);
    fireEvent.click(screen.getByLabelText('Require an approved review to publish'));
    fireEvent.click(screen.getByRole('button', { name: 'Save protection' }));
    await screen.findByText('Applied');
    expect(h.set).toHaveBeenCalledExactlyOnceWith('profile', { ...settings, required: true });
  });
  it('shows a pending loosening and its proposed settings', async () => {
    h.policy.mockResolvedValueOnce({ ...policy, effective: { ...settings, required: true } }).mockResolvedValue(pending);
    h.set.mockResolvedValue({ applied: false, policy: pending }); show(); await edit();
    fireEvent.click(screen.getByLabelText('Require an approved review to publish'));
    fireEvent.click(screen.getByRole('button', { name: 'Save protection' }));
    await screen.findByText('Pending protection change');
    expect(screen.getAllByText('Waiting for a second person').length).toBeGreaterThan(0);
    expect(h.set).toHaveBeenCalledWith('profile', settings);
  });
  it('shows the delay when effectiveAt is set', async () => {
    h.policy.mockResolvedValue({ ...pending, pendingChange: { ...pending.pendingChange, effectiveAt: '2026-11-11T10:00:00Z' } }); show();
    await screen.findByText(/Takes effect at .* unless cancelled/);
  });
  it('hides approval for the proposer and cancels the change', async () => {
    h.username = 'bob'; h.policy.mockResolvedValue(pending); h.cancel.mockResolvedValue(policy); show();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel policy change' }));
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull();
    await waitFor(() => expect(h.cancel).toHaveBeenCalledExactlyOnceWith('change'));
  });
  it('offers step-up on an approval refusal', async () => {
    h.policy.mockResolvedValue(pending); h.approve.mockRejectedValue(new GraphQLRequestError('Verify', 200, [{ message: 'Verify', extensions: { code: 'STEP_UP_REQUIRED' } }]));
    show(); fireEvent.click(await screen.findByRole('button', { name: 'Approve' }));
    await screen.findByText('Approving needs a fresh second factor');
    expect(h.approve).toHaveBeenCalledExactlyOnceWith('change');
  });
  it('hides editing for scoped callers', async () => {
    h.scoped = true; show(); await screen.findByText('No changes outside review have been recorded.');
    expect(screen.queryByRole('button', { name: 'Edit protection' })).toBeNull();
  });
});
