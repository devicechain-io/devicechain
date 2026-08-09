// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts). The real catalogs are wired
// in by importing the i18n config for its side effect, so every assertion below is
// on the STRING AN OPERATOR SEES rather than on a prop or a translation key.
//
// Only two seams are faked: the GraphQL transport (`gql`) and the auth context.
// Everything between them is the real code — the authority check, the refusal
// classification, and the absent-vs-zero display rule all run for real.
import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DecodedClaims } from '@devicechain/client';

// vi.mock factories are hoisted above the imports, so the doubles they close over
// have to be created in a hoisted block or they are still in their TDZ.
const { gqlMock, authState } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  authState: { claims: null as unknown },
}));

// Spread the real module so GraphQLRequestError, hasAuthority and the rest stay
// genuine: the point of this file is that the REAL hasAuthority and the REAL
// forbidden-classification decide what renders.
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => authState }));

import { GraphQLRequestError } from '@devicechain/client';
import { DeviceLocationPanel } from './DeviceLocationPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  authState.claims = null;
});

const claimsWith = (authorities: string[]): DecodedClaims => ({
  tenant: 'acme',
  username: 'operator@acme.test',
  roles: [],
  authorities,
  typ: 'access',
});

// The read-only viewer baseline the platform actually issues. location:read is
// deliberately NOT in it, which is exactly why the forbidden state has to exist.
const VIEWER = ['device:read', 'event:read', 'state:read', 'command:read', 'alarm:read'];
const VIEWER_PLUS_LOCATION = [...VIEWER, 'location:read'];

const NO_PERMISSION = 'You don’t have permission to view this device’s location.';
const NEVER_LOCATED = 'This device has not reported a position.';

const FULL_FIX = {
  id: '1',
  deviceToken: 'excavator-7',
  latitude: 34.052235,
  longitude: -118.243683,
  elevation: 71.5,
  accuracy: 4.2,
  speed: 13.4,
  heading: 275,
  occurredTime: '2026-08-09T12:00:00Z',
};

// The value rendered ALONGSIDE a label, not merely somewhere on the page. Asserting
// on the value alone would pass just as happily with latitude and longitude swapped —
// both numbers are still present — so the association is what gets asserted.
function valueFor(label: string): string | null | undefined {
  return screen.getByText(label).nextElementSibling?.textContent;
}

describe('DeviceLocationPanel', () => {
  it('shows every field the device actually reported', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    gqlMock.mockResolvedValue({ latestLocation: FULL_FIX });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText('Latitude')).toBeTruthy();
    expect(valueFor('Latitude')).toBe('34.052235');
    expect(valueFor('Longitude')).toBe('-118.243683');
    expect(valueFor('Elevation')).toBe('71.5 m');
    expect(valueFor('Accuracy')).toBe('± 4.2 m');
    expect(valueFor('Speed')).toBe('13.4 m/s');
    expect(valueFor('Heading')).toBe('275°');
    // The counterweight to the forbidden test below: a panel hard-wired to refuse
    // would satisfy that one perfectly and fail here.
    expect(screen.queryByText(NO_PERMISSION)).toBeNull();
  });

  // 🔴 An optional the device did not report must not be invented. A fix with no
  // compass has not reported due north; rendering `0°` would put a number on the
  // screen the platform deliberately declined to store, and nothing distinguishes
  // it from a device genuinely pointing north.
  it('omits an unreported optional rather than rendering it as 0', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    gqlMock.mockResolvedValue({
      latestLocation: {
        ...FULL_FIX,
        elevation: null,
        accuracy: null,
        speed: null,
        heading: null,
      },
    });

    const { container } = render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText('Latitude')).toBeTruthy();
    for (const label of ['Elevation', 'Accuracy', 'Speed', 'Heading']) {
      expect(screen.queryByText(label), `${label} was not reported and must not appear`).toBeNull();
    }
    // …and no zero-valued reading anywhere on the panel, however it were labelled.
    const shown = container.textContent ?? '';
    for (const invented of ['0 m', '± 0 m', '0 m/s', '0°']) {
      expect(shown.includes(invented), `panel invented a reading: ${invented}`).toBe(false);
    }
  });

  // 🔴 THE ONE THAT MATTERS. A member holding the whole read-only viewer set is
  // routinely refused position, by design. "No position recorded" would be a claim
  // about the DEVICE; a red error would be a claim about the PLATFORM. Neither is
  // true — the claim is about the CALLER, and it gets its own state.
  it('tells a user without location:read that they may not see it', async () => {
    authState.claims = claimsWith(VIEWER);

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(NO_PERMISSION)).toBeTruthy();
    // Not the empty state, which says something false about the device…
    expect(screen.queryByText(NEVER_LOCATED)).toBeNull();
    // …and the known-absent authority costs no round-trip at all.
    expect(gqlMock).not.toHaveBeenCalled();
  });

  // The claim is a snapshot: a token minted before a role change still advertises
  // an authority the server has since withdrawn. The refusal path has to land in
  // the same state, or a stale token turns a permission boundary into an error.
  it('lands in the same state when a stale claim is refused by the server', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    gqlMock.mockRejectedValue(
      new GraphQLRequestError('forbidden: missing required authority', 200, [
        { message: 'forbidden: missing required authority' },
      ]),
    );

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(NO_PERMISSION)).toBeTruthy();
    expect(gqlMock).toHaveBeenCalled();
    // The server's raw refusal text is never what the operator reads.
    expect(screen.queryByText('forbidden: missing required authority')).toBeNull();
  });

  it('says the device has no position only when the server says so', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    gqlMock.mockResolvedValue({ latestLocation: null });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(NEVER_LOCATED)).toBeTruthy();
    expect(screen.queryByText(NO_PERMISSION)).toBeNull();
  });

  // The counterweight to the refusal classification: a broken service must still
  // look broken. Swallowing every failure into the permission state would hide an
  // outage behind a sentence that reads like normal operation.
  it('still surfaces a non-authorization failure as an error', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    gqlMock.mockRejectedValue(new GraphQLRequestError('Request failed (503)', 503));

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText('Request failed (503)')).toBeTruthy();
    expect(screen.queryByText(NO_PERMISSION)).toBeNull();
    expect(screen.queryByText(NEVER_LOCATED)).toBeNull();
  });
});
