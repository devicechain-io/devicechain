// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import type { ReactNode } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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
const policy = { effective: { required: true } };
beforeEach(() => {
  vi.resetAllMocks(); h.policy.mockResolvedValue(policy); h.publish.mockResolvedValue(4);
  h.reviews.mockResolvedValue({ results: [{ token: 'approved-review', outcome: 'PASSED', approvedAt: '2026-11-10T10:00:00Z' }] });
});
afterEach(cleanup);
function show() { render(<MemoryRouter><VersionsPanel profileToken="profile" activeVersion={3} deviceTypeCount={1} onChanged={h.changed} /></MemoryRouter>); }
async function publish() {
  fireEvent.click(screen.getByRole('button', { name: 'Publish' }));
  const buttons = await screen.findAllByRole('button', { name: 'Publish' }); fireEvent.click(buttons[buttons.length - 1]);
}
describe('protected profile publishing', () => {
  it('passes the selected review and refreshes reviews after publishing', async () => {
    show(); await screen.findByRole('option', { name: /approved-rev/ });
    fireEvent.change(screen.getByLabelText('Approved review'), { target: { value: 'approved-review' } });
    await publish();
    await waitFor(() => expect(h.publish).toHaveBeenCalledExactlyOnceWith('profile', undefined, undefined, 'approved-review'));
    await waitFor(() => expect(h.reviews).toHaveBeenCalledTimes(2));
  });
  it('explains the missing review, links to review, and blocks publishing', async () => {
    h.reviews.mockResolvedValue({ results: [] }); show();
    await screen.findByText(/There are no approved reviews/);
    expect(screen.getByRole('link', { name: 'Change review' }).getAttribute('href')).toBe('/device-profiles/profile?tab=change-review');
    await publish();
    await waitFor(() => expect(h.toast).toHaveBeenCalledWith('Select an approved review of the current draft.', 'error'));
    expect(h.publish).not.toHaveBeenCalled();
  });
  it('preserves publishing when the policy cannot be read', async () => {
    h.policy.mockRejectedValue(new Error('Unavailable')); show();
    await waitFor(() => expect(h.policy).toHaveBeenCalled()); await publish();
    await waitFor(() => expect(h.publish).toHaveBeenCalledExactlyOnceWith('profile', undefined, undefined));
  });
});
