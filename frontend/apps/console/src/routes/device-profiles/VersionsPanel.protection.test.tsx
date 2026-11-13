// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import type { ReactNode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ policy: vi.fn(), reviews: vi.fn(), publish: vi.fn(), rollback: vi.fn(), toast: vi.fn(), changed: vi.fn() }));
vi.mock('@/lib/api/automation-reviews', () => ({ getAutomationReviewPolicy: h.policy, listApprovedReviewsForDraft: h.reviews }));
vi.mock('@/lib/api/device-management', () => ({ publishDeviceProfile: h.publish, rollbackDeviceProfile: h.rollback,
  listDeviceProfileVersions: async () => [], getCalculatedMetricPlan: async () => ({ calculated: [] }) }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { username: 'ann', authorities: ['device:write'], scopeAnchors: [] } }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => async () => true }));
vi.mock('./BackfillSection', () => ({ BackfillSection: () => null }));
vi.mock('./CalculatedHealthSection', () => ({ CalculatedHealthSection: () => null }));
vi.mock('./ThingModelDownload', () => ({ ThingModelDownload: () => null }));
vi.mock('@/components/ui/select', () => ({
  Select: ({ id, value, onChange, children }: { id?: string; value: string; onChange: (value: string) => void; children: ReactNode }) =>
    <select id={id} value={value} onChange={e => onChange(e.target.value)}>{children}</select>,
  SelectItem: ({ value, children }: { value: string; children: ReactNode }) => <option value={value}>{children}</option>,
}));
import { VersionsPanel } from './VersionsPanel';
// Warm the production lazy imports during module setup, outside each test's
// timeout budget. Cold Vite transforms on CI are not part of publishing behavior.
import '@/routes/automation-reviews/PublishReviewSelect';
import '@/lib/api/automation-reviews';
const policy = { effective: { required: true } };
beforeEach(() => {
  vi.resetAllMocks(); h.policy.mockResolvedValue(policy); h.publish.mockResolvedValue(4);
  h.reviews.mockResolvedValue({ results: [{ token: 'approved-review', outcome: 'PASSED', approvedAt: '2026-11-10T10:00:00Z' }] });
});
afterEach(cleanup);
async function show() {
  render(<MemoryRouter><VersionsPanel profileToken="profile" activeVersion={3} deviceTypeCount={1} onChanged={h.changed} /></MemoryRouter>);
  await act(async () => {
    // Settle the policy (including rejection) and the lazy review selector before
    // interacting; a mock being called does not mean useQuery has settled.
    await vi.dynamicImportSettled();
  });
}
async function publish() {
  fireEvent.click(screen.getByRole('button', { name: 'Publish' }));
  const drawer = await screen.findByRole('dialog', { name: 'Publish profile' });
  fireEvent.click(within(drawer).getByRole('button', { name: 'Publish' }));
}

describe('protected profile publishing', () => {
  it('passes the selected review and refreshes reviews after publishing', async () => {
    await show(); await screen.findByRole('option', { name: /approved-rev/ });
    fireEvent.change(screen.getByLabelText('Approved review'), { target: { value: 'approved-review' } });
    await publish();
    await waitFor(() => expect(h.publish).toHaveBeenCalledExactlyOnceWith('profile', undefined, undefined, 'approved-review'));
    await waitFor(() => expect(h.reviews).toHaveBeenCalledTimes(2));
    await screen.findByRole('option', { name: /approved-rev/ });
    expect((screen.getByLabelText('Approved review') as HTMLSelectElement).value).toBe('');
    expect(h.changed).toHaveBeenCalledTimes(1);
  });
  it('explains the missing review, links to review, and blocks publishing', async () => {
    h.reviews.mockResolvedValue({ results: [] }); await show();
    await screen.findByText(/There are no approved reviews/);
    expect(screen.getByRole('link', { name: 'Change review' }).getAttribute('href')).toBe('/device-profiles/profile?tab=change-review');
    await publish();
    await waitFor(() => expect(h.toast).toHaveBeenCalledWith('Select an approved review of the current draft.', 'error'));
    expect(h.publish).not.toHaveBeenCalled();
  });
  it('preserves publishing when the policy cannot be read', async () => {
    h.policy.mockRejectedValue(new Error('Unavailable')); await show();
    expect(h.policy).toHaveBeenCalled(); await publish();
    await waitFor(() => expect(h.publish).toHaveBeenCalledExactlyOnceWith('profile', undefined, undefined));
  });
});
