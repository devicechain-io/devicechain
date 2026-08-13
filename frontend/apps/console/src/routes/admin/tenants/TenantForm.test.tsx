// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 updateTenant is a FULL REPLACE of a tenant's governance overrides — a field the
// request omits is written as NULL, which clears the override back to the tier's value
// and then the platform default. So every override this form does not carry is an
// override the next save destroys, no matter how unrelated the edit that triggered it.
// Renaming a tenant is enough. There is no warning and no diff; the toast says saved.
//
// That is not hypothetical: `heldCommandCeiling` shipped on the admin API and in the
// full-replace set while this form still built its governance payload from the ingest,
// outbound and AI fields alone, so an operator-set held-command ceiling survived exactly
// until the next time anybody touched the tenant in the console.
//
// The transport is the only seam faked. The form, the tier picker and the request it
// builds all run for real, which is the only reason asserting on what went out means
// anything.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { TenantForm } from './TenantForm';
import type { AdminTenant } from '@/lib/api/admin';

const TIER = { token: 'gold', name: 'Gold', color: '#d4af37' };

function tenant(overrides: Partial<AdminTenant> = {}): AdminTenant {
  return {
    id: 't-1',
    token: 'acme',
    name: 'Acme Corp',
    enabled: true,
    purgeState: 'NONE',
    purgeEpoch: null,
    tier: TIER,
    config: null,
    ingestMessagesPerSecond: 500,
    ingestBurst: 1000,
    outboundMessagesPerSecond: null,
    outboundBurst: null,
    aiExternalEnabled: false,
    aiInferenceRequestsPerMinute: null,
    aiInferenceBurst: null,
    heldCommandCeiling: 2500,
    effectiveSettings: [],
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-01T00:00:00Z',
    ...overrides,
  } as AdminTenant;
}

/** The mutation requests actually sent — the tier picker's query is not one. */
function requests(): Record<string, unknown>[] {
  return gqlMock.mock.calls
    .filter((call) => call[0] === 'user-management/admin')
    .map((call) => (call[2] as { request?: Record<string, unknown> } | undefined)?.request)
    .filter((r): r is Record<string, unknown> => r != null);
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockImplementation(() =>
    Promise.resolve({
      // The tier picker. Save stays DISABLED until a tier is selected, so an empty
      // list here would leave every assertion below about a button that never fired.
      tenantTiers: [{ id: 'tier-1', ...TIER, description: null, displayOrder: 1 }],
      updateTenant: {},
      createTenant: {},
    }),
  );
});

/** Renders the real tabbed edit form (Effective tab supplied, as the detail page does). */
function renderEdit(entity: AdminTenant) {
  render(<TenantForm tenant={entity} onDone={vi.fn()} effectiveSettingsPanel={<div />} />);
}

async function clickSave() {
  const save = await screen.findByRole('button', { name: 'Save changes' });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(save);
  await waitFor(() => expect(requests()).toHaveLength(1));
  return requests()[0];
}

describe('editing a tenant', () => {
  it('preserves the governance overrides it is not editing', async () => {
    renderEdit(tenant());
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Acme Industrial' } });
    const request = await clickSave();

    // The edit itself has to arrive, or "nothing was destroyed" would be satisfied
    // by a form that saved nothing at all.
    expect(request.name).toBe('Acme Industrial');
    // …and the overrides the operator never opened the Settings tab to see survive it.
    expect(request.heldCommandCeiling).toBe(2500);
    expect(request.ingestMessagesPerSecond).toBe(500);
    expect(request.ingestBurst).toBe(1000);
  });

  // The counterweight. Without it the assertion above would pass just as well against
  // a form that had begun inventing a ceiling for a tenant that never carried one —
  // which under a full replace is a silently imposed bound, not a preserved one.
  it('sends no held-command ceiling for a tenant that has no override', async () => {
    renderEdit(tenant({ heldCommandCeiling: null }));
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Acme Industrial' } });
    const request = await clickSave();

    expect(request.name).toBe('Acme Industrial');
    expect(request.heldCommandCeiling).toBeUndefined();
  });

  it('sends the held-command ceiling the operator typed', async () => {
    renderEdit(tenant());
    // Radix activates a tab on mousedown, not click.
    fireEvent.mouseDown(await screen.findByRole('tab', { name: 'Settings' }));
    fireEvent.change(await screen.findByLabelText('Held command ceiling'), { target: { value: '400' } });
    const request = await clickSave();

    expect(request.heldCommandCeiling).toBe(400);
  });
});
