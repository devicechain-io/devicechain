// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';
import type { AutomationReview } from '@/lib/api/automation-reviews';

const h = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn(), matching: vi.fn(), run: vi.fn(), approve: vi.fn(), reject: vi.fn(),
  policy: vi.fn(), records: vi.fn(), rules: vi.fn(), toast: vi.fn(), clipboard: vi.fn(), authorities: ['device:write'] as string[] }));
vi.mock('@/lib/api/automation-reviews', () => ({ getAutomationReviewPolicy: h.policy, listAutomationReviewBreakGlass: h.records, getAutomationReview: h.get, listAutomationReviews: h.list, matchingDraftReviewTokens: h.matching,
  runAutomationReview: h.run, approveAutomationReview: h.approve, rejectAutomationReview: h.reject }));
vi.mock('@/lib/api/device-management', () => ({ listDetectionRules: h.rules }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { tenant: 'tenant', username: 'ann', authorities: h.authorities, scopeAnchors: [] }, identityToken: 'session' }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
import AutomationReviewPage from './AutomationReviewPage';
import { AutomationReviewPanel } from './AutomationReviewPanel';

const at = '2026-11-10T10:00:00Z';
const review: AutomationReview = {
  consumedVersion: null, token: 'review-1', profileToken: 'profile-1', ruleToken: 'rule-1', kind: 'RULE_CHANGE', state: 'RAN', outcome: 'PASSED',
  reviewDigest: 'sha256:review-evidence', draftDigest: 'draft-digest', baselineDigest: 'base-digest', baselineVersion: 3,
  createdBy: 'bob', createdAt: at, requiredApprovals: 2, approvals: [], draftAuthors: ['bob'], profileChanges: [], reasons: [],
  approvedAt: null, expiresAt: null, decidedBy: null, decidedAt: null, note: null, shiftWindowSeconds: null, resultDigest: null, result: null,
  deviceTokens: ['device-1'], windows: [], expectationInputs: [], matchesCurrentDraft: true,
  fidelity: { status: 'SUPPORTED', label: 'replay of recorded inputs', reasons: [] },
  expectations: [{ index: 0, name: 'Alarm within five minutes', window: 'INCIDENT', verdict: 'PASS', baseline: 'FAIL',
    reason: 'Alarm raised', baselineReason: 'No alarm', evidence: [{ device: 'device-1', at, value: 42, spanSeconds: 0 }] }],
  windowResults: [{ role: 'INCIDENT', start: at, end: '2026-11-10T11:00:00Z', leadInStart: at, stream: 'stream-1',
    firstSeq: '1', lastSeq: '20', inputDigest: 'input-digest', degraded: [], changesTruncated: false,
    counts: { unchanged: 0, added: 0, removed: 0, shifted: 0, contentChanged: 1 }, devices: [],
    changes: [{ kind: 'CONTENT_CHANGED', device: 'device-1', subject: 'sendCommand', discriminator: 'set-output', baseAt: at, draftAt: at,
      shiftSeconds: 0, contentChanged: true, afterDivergence: true,
      baseContent: { payload: '{"level":1}', severity: 'LOW', relativeExpirySeconds: null, expiryLabel: null, configDigest: null, bodyDigest: null, method: null },
      draftContent: { payload: '{"level":2}', severity: 'HIGH', relativeExpirySeconds: null, expiryLabel: null, configDigest: null, bodyDigest: null, method: null },
      reading: { device: 'device-1', at, value: 42, spanSeconds: 0 } }] }],
  inputDigest: 'input-digest', engineBuild: 'engine-1',
};
const listReview = { token: review.token, profileToken: review.profileToken, ruleToken: review.ruleToken, kind: review.kind,
  state: review.state, outcome: review.outcome, createdBy: review.createdBy, createdAt: review.createdAt };
beforeEach(() => {
  vi.resetAllMocks(); h.authorities = ['device:write'];
  h.policy.mockResolvedValue(null); h.records.mockResolvedValue([]);
  h.get.mockResolvedValue(review); h.list.mockResolvedValue({ results: [listReview], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } });
  h.matching.mockResolvedValue(new Set([review.token]));
  h.rules.mockResolvedValue([]); h.approve.mockResolvedValue({ token: review.token, state: 'APPROVED' });
  h.reject.mockResolvedValue({ token: review.token, state: 'REJECTED' }); h.clipboard.mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: h.clipboard } });
});
afterEach(cleanup);
function show(panel = false) {
  return render(<MemoryRouter initialEntries={[panel ? '/profile' : '/automation-reviews/review-1']}><Routes>
    <Route path="/automation-reviews/:token" element={<AutomationReviewPage />} />
    <Route path="/profile" element={<AutomationReviewPanel profileToken="profile-1" />} />
  </Routes></MemoryRouter>);
}

describe('review detail', () => {
  it('shows the version that consumed the review', async () => {
    h.get.mockResolvedValue({ ...review, consumedVersion: 4 }); show();
    await screen.findByText('Consumed by version 4');
  });
  it('renders the outcome, comparative expectation, and changed payloads', async () => {
    show(); await screen.findByText('PASSED');
    expect(screen.getByText('was FAIL, now PASS')).toBeTruthy();
    expect(screen.getByText('Alarm within five minutes')).toBeTruthy();
    const change = screen.getByRole('row', { name: /Content changed/ });
    expect(change.textContent).toContain('{"level":1}'); expect(change.textContent).toContain('{"level":2}');
    expect(change.textContent).toContain('LOW'); expect(change.textContent).toContain('HIGH');
    expect(screen.getByRole('link', { name: 'profile-1' }).getAttribute('href')).toBe('/device-profiles/profile-1');
  });
  it('approves the displayed review digest and refreshes', async () => {
    show(); fireEvent.click(await screen.findByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(h.approve).toHaveBeenCalledExactlyOnceWith('review-1', review.reviewDigest));
    await waitFor(() => expect(h.get).toHaveBeenCalledTimes(2));
  });
  it('shows the existing step-up prompt', async () => {
    h.approve.mockRejectedValue(new GraphQLRequestError('Fresh factor required', 200,
      [{ message: 'Fresh factor required', extensions: { code: 'STEP_UP_REQUIRED' } }]));
    show(); fireEvent.click(await screen.findByRole('button', { name: 'Approve' }));
    await screen.findByText('Approving needs a fresh second factor');
    expect(screen.getByRole('button', { name: 'Verify code' })).toBeTruthy();
  });
  it('copies the share URL and toasts', async () => {
    show(); fireEvent.click(await screen.findByRole('button', { name: 'Copy link' }));
    await waitFor(() => expect(h.clipboard).toHaveBeenCalledExactlyOnceWith(window.location.origin + '/automation-reviews/review-1'));
    expect(h.toast).toHaveBeenCalledWith('Review link copied.');
  });
  it('hides write actions without device:write', async () => {
    h.authorities = ['device:read']; show(); await screen.findByText('PASSED');
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Reject' })).toBeNull();
  });
  it('allows rejecting an approved review with a note', async () => {
    h.get.mockResolvedValue({ ...review, state: 'APPROVED' }); show();
    fireEvent.click(await screen.findByRole('button', { name: 'Reject' }));
    fireEvent.change(screen.getByLabelText('Rejection note (optional)'), { target: { value: 'Please revise.' } });
    fireEvent.click(screen.getByRole('button', { name: 'Submit rejection' }));
    await waitFor(() => expect(h.reject).toHaveBeenCalledExactlyOnceWith('review-1', 'Please revise.'));
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull();
  });
});

describe('run review panel', () => {
  it('marks current rows using the filtered token set', async () => {
    h.list.mockResolvedValue({ results: [listReview, { ...listReview, token: 'stale-review' }], pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 } });
    show(true);
    const current = await screen.findByRole('row', { name: /review-1/ });
    expect(current.textContent).toContain('Matches current draft');
    expect(screen.getByRole('row', { name: /stale-review/ }).textContent).toContain('Draft changed');
    expect(h.matching).toHaveBeenCalledExactlyOnceWith('profile-1');
  });
  it('refuses 51 devices', async () => {
    show(true); await screen.findByRole('link', { name: 'review-1' });
    fireEvent.change(screen.getByLabelText('Device tokens'), { target: { value: Array.from({ length: 51 }, (_, i) => `device-${i}`).join(',') } });
    expect(screen.getByRole('alert').textContent).toContain('Select at most 50 devices.');
    expect((screen.getByRole('button', { name: 'Run review' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.submit(screen.getByRole('button', { name: 'Run review' }).closest('form')!);
    expect(h.run).not.toHaveBeenCalled();
  });
  it('submits UTC windows and a fresh request ID, then navigates to the review', async () => {
    h.run.mockResolvedValue({ token: 'review-1' }); show(true);
    await screen.findByRole('link', { name: 'review-1' });
    fireEvent.change(screen.getByLabelText('Device tokens'), { target: { value: 'device-1' } });
    for (const role of ['INCIDENT', 'NORMAL']) {
      fireEvent.change(document.getElementById(`review-${role}-start`)!, { target: { value: '2026-11-10T10:00' } });
      fireEvent.change(document.getElementById(`review-${role}-end`)!, { target: { value: '2026-11-10T11:00' } });
    }
    fireEvent.submit(screen.getByRole('button', { name: 'Run review' }).closest('form')!);
    await waitFor(() => expect(h.run).toHaveBeenCalledTimes(1));
    expect(h.run.mock.calls[0][0]).toEqual({ profileToken: 'profile-1', ruleToken: undefined, deviceTokens: ['device-1'], expectations: [],
      requestId: expect.any(String), windows: ['INCIDENT', 'NORMAL'].map(role => ({ role,
        start: new Date('2026-11-10T10:00').toISOString(), end: new Date('2026-11-10T11:00').toISOString() })) });
    await screen.findByText('was FAIL, now PASS');
  });
});
