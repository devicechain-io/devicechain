// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';
import { MembershipPanel } from './MembershipPanel';

const { listGroupMembers } = vi.hoisted(() => ({ listGroupMembers: vi.fn() }));
vi.mock('@/lib/api/relationships', () => ({
  listGroupMembers,
  addGroupMembers: vi.fn(),
  removeGroupMembers: vi.fn(),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));

afterEach(cleanup);
beforeEach(() => { listGroupMembers.mockReset(); });
const props = { groupType: 'group', groupToken: 'fleet', memberType: 'device', memberI18nKey: 'device' };

it('pages members beyond the former cutoff and displays the total', async () => {
  listGroupMembers.mockImplementation(async (_type, _token, { pageNumber, pageSize }) => ({
    results: Array.from({ length: Math.min(pageSize, 1001 - (pageNumber - 1) * pageSize) }, (_, i) => ({
      token: `edge-${pageNumber}-${i}`, target: { token: `member-${(pageNumber - 1) * pageSize + i}` },
    })),
    pagination: { pageStart: (pageNumber - 1) * pageSize + 1, pageEnd: Math.min(pageNumber * pageSize, 1001), totalRecords: 1001 },
  }));
  const { rerender } = render(<MembershipPanel {...props} loadCandidates={async () => []} />);
  await screen.findAllByText('member-0');
  for (let page = 2; page <= 21; page++) {
    fireEvent.click(screen.getByRole('button', { name: /next/i }));
    await screen.findAllByText(`member-${(page - 1) * 50}`);
  }
  expect(listGroupMembers).toHaveBeenLastCalledWith('group', 'fleet', { pageNumber: 21, pageSize: 50 });
  expect(screen.queryByText('member-0')).toBeNull();
  expect(screen.getByText(/1001/)).toBeTruthy();
  expect(screen.getByRole('button', { name: /next/i }).hasAttribute('disabled')).toBe(true);
  rerender(<MembershipPanel {...props} groupToken="other" loadCandidates={async () => []} />);
  await screen.findAllByText('member-0');
  expect(listGroupMembers).toHaveBeenCalledWith('group', 'other', { pageNumber: 1, pageSize: 50 });
  expect(screen.queryByText('member-1000')).toBeNull();
  // Twenty page turns take a few seconds alone and more on a loaded runner.
}, 20_000);

it('shows a member read refusal instead of the empty state', async () => {
  listGroupMembers.mockRejectedValue(new GraphQLRequestError('Members refused', 403));
  render(<MembershipPanel {...props} loadCandidates={async () => []} />);
  await screen.findAllByText('Members refused');
  expect(screen.queryByRole('button', { name: /next/i })).toBeNull();
});

it('shows a candidate read refusal and disables adding', async () => {
  listGroupMembers.mockResolvedValue({ results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } });
  render(<MembershipPanel {...props} loadCandidates={async () => { throw new GraphQLRequestError('Candidates refused', 403); }} />);
  await screen.findByText('Candidates refused');
  await waitFor(() => expect(screen.getByRole('button', { name: /^add$/i }).hasAttribute('disabled')).toBe(true));
});
