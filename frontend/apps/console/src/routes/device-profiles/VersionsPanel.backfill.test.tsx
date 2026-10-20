// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The offer to backfill that follows a publish, driven through the real Versions panel: only the
// GraphQL transport, the auth claims, the toast and the confirm dialog are faked.
//
// What this file pins:
//   - publishing a version that ADDS a calculated metric asks, naming the metric, and a yes opens
//     the backfill dialog on that version;
//   - a no opens nothing, and a version that adds nothing asks nothing;
//   - a customer-scoped caller is never asked, and the publish does not even look at the plan.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DecodedClaims } from '@devicechain/client';

const { gqlMock, confirmMock, authState } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  confirmMock: vi.fn(),
  authState: { claims: null as unknown },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => authState }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => confirmMock }));

import { VersionsPanel } from './VersionsPanel';

function claims(authorities: string[], scopeAnchors: string[] = []): DecodedClaims {
  return { authorities, scopeAnchors } as unknown as DecodedClaims;
}

type Calc = { key: string; unit: string | null; newInVersion: boolean; changedSinceEarlier: boolean };

function install(calculatedIn4: Calc[]) {
  const calls: { op: string; vars: Record<string, unknown> }[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, unknown>) => {
    const op = /(?:query|mutation) (\w+)/.exec(String(doc))?.[1] ?? '';
    calls.push({ op, vars });
    switch (op) {
      case 'DeviceProfileVersions':
        return Promise.resolve({
          deviceProfileVersions: [{ version: 3, label: null, description: null, publishedAt: '2026-10-10T12:00:00Z', publishedBy: 'a' }],
        });
      case 'PublishDeviceProfile':
        return Promise.resolve({ publishDeviceProfile: { version: 4 } });
      case 'CalculatedMetricPlan': {
        const v = (vars.version as number | undefined) ?? 3;
        return Promise.resolve({
          calculatedMetricPlan: { version: v, activeSince: null, calculated: v === 4 ? calculatedIn4 : [] },
        });
      }
      case 'CalculatedMetricBackfills':
        return Promise.resolve({ calculatedMetricBackfills: { nodes: [], nextCursor: null } });
      default:
        return Promise.reject(new Error(`unexpected operation ${op}`));
    }
  });
  return calls;
}

const ADDS = [
  { key: 'power_kw', unit: 'kW', newInVersion: true, changedSinceEarlier: false },
  { key: 'energy_kwh', unit: 'kWh', newInVersion: false, changedSinceEarlier: true },
];

async function publish() {
  render(<VersionsPanel profileToken="meter" activeVersion={3} deviceTypeCount={1} onChanged={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Publish' }));
  const buttons = await screen.findAllByRole('button', { name: 'Publish' });
  fireEvent.click(buttons[buttons.length - 1]);
}

beforeEach(() => {
  gqlMock.mockReset();
  confirmMock.mockReset();
  authState.claims = claims(['device:write', 'device:read', 'event:read']);
});
afterEach(cleanup);

describe('the backfill offer after a publish', () => {
  it('asks, naming the added metric, and a yes opens the dialog on the new version', async () => {
    confirmMock.mockResolvedValue(true);
    const calls = install(ADDS);
    await publish();

    await waitFor(() => expect(confirmMock).toHaveBeenCalledTimes(1));
    const asked = confirmMock.mock.calls[0][0] as { description: string };
    expect(asked.description).toContain('power_kw');
    // Only what the version ADDS is offered; a metric that merely changed is not named.
    expect(asked.description).not.toContain('energy_kwh');

    expect(await screen.findByRole('button', { name: 'Backfill every device' })).toBeTruthy();
    expect(calls.some((c) => c.op === 'PublishDeviceProfile')).toBe(true);
    expect(calls.filter((c) => c.op === 'CalculatedMetricPlan' && c.vars.version === 4).length).toBeGreaterThanOrEqual(2);
  });

  it('opens nothing on a no', async () => {
    confirmMock.mockResolvedValue(false);
    install(ADDS);
    await publish();
    await waitFor(() => expect(confirmMock).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole('button', { name: 'Backfill every device' })).toBeNull();
  });

  it('asks nothing when the version adds no calculated metric', async () => {
    confirmMock.mockResolvedValue(true);
    const calls = install([{ ...ADDS[1] }]);
    await publish();
    await waitFor(() => expect(calls.some((c) => c.op === 'CalculatedMetricPlan' && c.vars.version === 4)).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(confirmMock).not.toHaveBeenCalled();
  });

  it('never asks a customer-scoped caller, and does not look at the plan', async () => {
    authState.claims = claims(['device:write', 'device:read', 'event:read'], ['customer:acme']);
    confirmMock.mockResolvedValue(true);
    const calls = install(ADDS);
    await publish();
    await waitFor(() => expect(calls.some((c) => c.op === 'PublishDeviceProfile')).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(confirmMock).not.toHaveBeenCalled();
    expect(calls.some((c) => c.op === 'CalculatedMetricPlan')).toBe(false);
    expect(calls.some((c) => c.op === 'CalculatedMetricBackfills')).toBe(false);
  });
});
