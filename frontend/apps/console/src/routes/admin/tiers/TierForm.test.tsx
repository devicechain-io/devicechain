// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THE ONE SPELLING OF "LEAVE THE TIER'S SETTINGS ALONE" INVERTED WHEN updateTenantTier
// BECAME A PARTIAL UPDATE, AND ONLY THIS FILE WOULD NOTICE.
//
// buildTierConfigPatch returns UNDEFINED to mean "say nothing about settings" — the case
// where the dimensions query is in flight or failed, so the editor never rendered any
// fields and the form has no business claiming to know what the tier sells. That decision
// is unit-tested in tierConfig.test.ts.
//
// What was NOT tested is the hop from that decision to the wire, and it is where the
// meaning changed. Under the old API `config: null` decoded to the same nil pointer an
// omitted key did, so the form wrote `config: config ?? null` and the two spellings agreed.
// Under the three-state semantic they are opposites: null CLEARS the tier's settings, which
// drops every tenant at it to the platform default within a minute, with no error and no
// log — the exact silent re-pricing buildTierConfigPatch exists to prevent, arriving through
// the line that was written to prevent it.
//
// So the assertion is about the KEY, not the value: `config` must be ABSENT from the request
// when the editor never rendered. JSON.stringify drops an undefined property, so an omitted
// key is what actually leaves the browser — but only if the form leaves it undefined, and a
// `?? null` anywhere on the path silently converts it back.
//
// The transport is the only seam faked; the form and the request it builds run for real,
// which is the only reason asserting on what went out means anything.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: false, isIdentityAuthenticated: true }),
}));

import { TierForm } from './TierForm';
import type { AdminTenantTierDetail } from '@/lib/api/admin';

function tier(overrides: Partial<AdminTenantTierDetail> = {}): AdminTenantTierDetail {
  return {
    id: 'tier-1',
    token: 'gold',
    name: 'Gold',
    description: 'The gold packaging',
    // A real ceiling, so "the settings survived" is an observation rather than the
    // absence of one: a tier with no config could not demonstrate preservation at all.
    config: '{"ingestMessagesPerSecond":2000}',
    color: 'amber',
    displayOrder: 0,
    tenantCount: 3,
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-01T00:00:00Z',
    ...overrides,
  };
}

// requestFor pulls the variables of the first updateTenantTier call out of the mock.
// The document is matched by the mutation NAME rather than by call index, because the
// form also issues the dimension and palette queries and their order is not this test's
// business.
function updateRequest(): Record<string, unknown> | undefined {
  for (const call of gqlMock.mock.calls) {
    const doc = String(call[1]);
    if (doc.includes('mutation UpdateTenantTier')) {
      return (call[2] as { request: Record<string, unknown> }).request;
    }
  }
  return undefined;
}

describe('TierForm', () => {
  beforeEach(() => {
    gqlMock.mockReset();
    gqlMock.mockImplementation(async (_area: unknown, document: unknown) => {
      const doc = String(document);
      // 🔴 THE DIMENSIONS QUERY ANSWERS EMPTY, WHICH IS THE WHOLE FIXTURE. It is what puts
      // the settings editor in the state buildTierConfigPatch refuses to speak for — the
      // in-flight-or-failed case a real operator hits when the admin plane is slow.
      if (doc.includes('GovernanceDimensions')) return { governanceDimensions: [] };
      if (doc.includes('TierColorPalette')) return { tierColorPalette: { colors: ['amber'] } };
      if (doc.includes('mutation UpdateTenantTier')) return { updateTenantTier: tier() };
      return {};
    });
  });
  afterEach(cleanup);

  it('omits config entirely when the settings editor never rendered', async () => {
    render(<TierForm tier={tier()} onDone={() => {}} />);

    const save = await screen.findByRole('button', { name: /save/i });
    fireEvent.click(save);

    await waitFor(() => expect(updateRequest()).toBeDefined());
    const request = updateRequest()!;

    // 🔴 ABSENT, not null. `toBeUndefined()` alone would pass for a key holding undefined
    // AND for one holding nothing, which is fine here — both serialize away — but a null
    // must fail, and asserting the key is not present is what says so unambiguously.
    expect('config' in request && request.config !== undefined).toBe(false);

    // The counterweight: the save still happened and still carried the fields the form
    // does own. Without it, a form that submitted nothing at all would satisfy the
    // assertion above — the most convincing kind of green there is.
    expect(request.name).toBe('Gold');
    expect(request.color).toBe('amber');
  });
});
