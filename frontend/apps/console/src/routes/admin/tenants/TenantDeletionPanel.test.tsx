// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const { getTenantDeletionMock } = vi.hoisted(() => ({ getTenantDeletionMock: vi.fn() }));
vi.mock('@/lib/api/admin', () => ({ getTenantDeletion: (...a: unknown[]) => getTenantDeletionMock(...a) }));

import { TenantDeletionPanel } from './TenantDeletionPanel';

afterEach(cleanup);

const LONG =
  'the object store still holds 2 of this tenant\'s uploaded asset(s), and this instance has no object storage configured — so the reference is known and the object cannot be reached from here.';

function record(stores: unknown[]) {
  return {
    token: 'acme', epoch: '2026-08-04T00:00:00Z', completedAt: null, rowsErased: 0,
    awaiting: 'STORES', elapsesAt: null, blockedBy: [], stores,
  };
}

function store(over: Record<string, unknown>) {
  return { store: 'blob', complete: false, rowsErased: 0, retaining: null, lastError: null, note: null, attemptedAt: null, cleanSince: null, ...over };
}

describe('TenantDeletionPanel storage rows', () => {
  it('cuts a long explanation to its lead clause and keeps the full text behind a disclosure', async () => {
    getTenantDeletionMock.mockResolvedValue(record([store({ retaining: LONG })]));
    render(<TenantDeletionPanel token="acme" />);
    // WHAT is held and WHY, in the server's own words, not a generic label.
    const lead = await screen.findByText(
      "the object store still holds 2 of this tenant's uploaded asset(s), and this instance has no object storage configured…",
    );
    expect(lead.closest('details')).toBeNull();
    expect(screen.getByText(LONG).closest('details')).not.toBeNull();
    expect(screen.getByText('Full explanation')).toBeTruthy();
  });

  it('shows a short explanation as written, with no disclosure', async () => {
    getTenantDeletionMock.mockResolvedValue(record([store({ retaining: 'an object remains' })]));
    const { container } = render(<TenantDeletionPanel token="acme" />);
    expect(await screen.findByText(/an object remains/)).toBeTruthy();
    expect(container.querySelector('details')).toBeNull();
  });
});
