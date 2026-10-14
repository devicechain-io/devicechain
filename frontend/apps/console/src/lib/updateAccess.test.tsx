// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Presence detection and authority helpers for the update-management area.
//
// The presence tests are the fail-CLOSED contract: unlike the rest of the console's area
// gating (which treats an unanswered question as "carry on"), an opt-in area's surfaces
// stay hidden unless the area is known to be deployed. 'error' is therefore a case of
// its own, with a control showing the same harness DOES reveal the surface when the
// area is reported.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, cleanup } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { DecodedClaims } from '@devicechain/client';
import { AreaCapabilityProvider } from '@/lib/capabilities';
import {
  canReadArtifacts,
  canTrustArtifacts,
  canWriteArtifacts,
  useUpdateAccess,
} from '@/lib/updateAuthority';

const listFunctionalAreas = vi.fn();
const listAdminFunctionalAreas = vi.fn();
const auth = {
  isAuthenticated: true,
  isIdentityAuthenticated: false,
  isLoading: false,
  claims: { authorities: [] as string[] } as DecodedClaims | null,
};

vi.mock('@/lib/api/user-management', () => ({
  listFunctionalAreas: (...a: unknown[]) => listFunctionalAreas(...a),
}));
vi.mock('@/lib/api/admin', () => ({
  listAdminFunctionalAreas: (...a: unknown[]) => listAdminFunctionalAreas(...a),
}));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => auth }));

const claimsWith = (...authorities: string[]) => ({ authorities }) as unknown as DecodedClaims;

function Probe() {
  const a = useUpdateAccess();
  return (
    <>
      <span data-testid="access">
        {[a.present, a.canRead, a.canWrite, a.canTrust].map((b) => (b ? '1' : '0')).join('')}
      </span>
      <span data-testid="status">{a.status}</span>
    </>
  );
}

function renderProbe() {
  render(
    <MemoryRouter>
      <AreaCapabilityProvider>
        <Probe />
      </AreaCapabilityProvider>
    </MemoryRouter>,
  );
}

afterEach(cleanup);
beforeEach(() => {
  listFunctionalAreas.mockReset();
  listAdminFunctionalAreas.mockReset();
  auth.isAuthenticated = true;
  auth.isIdentityAuthenticated = false;
  auth.isLoading = false;
  auth.claims = claimsWith('artifact:read', 'artifact:write', 'artifact:trust');
  vi.spyOn(console, 'warn').mockImplementation(() => {});
});

describe('update-management presence (fail closed)', () => {
  it('is present, and grants by authority, when the instance runs the area', async () => {
    listFunctionalAreas.mockResolvedValue(['user-management', 'update-management']);
    renderProbe();
    await waitFor(() => expect(screen.getByTestId('access').textContent).toBe('1111'));
  });

  it('hides everything when the area is absent, whatever the caller holds', async () => {
    listFunctionalAreas.mockResolvedValue(['user-management', 'device-management']);
    auth.claims = claimsWith('*');
    renderProbe();
    // "0000" is also the initial state, so the status is what proves the absent answer
    // was actually received rather than still pending.
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('absent'));
    expect(screen.getByTestId('access').textContent).toBe('0000');
  });

  it('hides everything when discovery errors, and says so', async () => {
    listFunctionalAreas.mockRejectedValue(new Error('network down'));
    renderProbe();
    await waitFor(() => expect(console.warn).toHaveBeenCalled());
    expect(screen.getByTestId('access').textContent).toBe('0000');
    // A routed page must not read this as "not deployed": the answer is unknown.
    expect(screen.getByTestId('status').textContent).toBe('unknown');
  });

  it('hides everything on an implausible reply (no user-management in the set)', async () => {
    listFunctionalAreas.mockResolvedValue(['update-management']);
    renderProbe();
    await waitFor(() => expect(console.warn).toHaveBeenCalled());
    expect(screen.getByTestId('access').textContent).toBe('0000');
  });

  it('hides everything while signed out (nothing can be asked)', async () => {
    auth.isAuthenticated = false;
    renderProbe();
    expect(listFunctionalAreas).not.toHaveBeenCalled();
    expect(screen.getByTestId('access').textContent).toBe('0000');
  });

  it('keeps authority separate once present: read only, no write or trust', async () => {
    listFunctionalAreas.mockResolvedValue(['user-management', 'update-management']);
    auth.claims = claimsWith('artifact:read');
    renderProbe();
    await waitFor(() => expect(screen.getByTestId('access').textContent).toBe('1100'));
  });
});

describe('artifact authority helpers', () => {
  it('maps each helper to exactly its own authority', () => {
    const read = claimsWith('artifact:read');
    const write = claimsWith('artifact:write');
    const trust = claimsWith('artifact:trust');
    expect([canReadArtifacts(read), canWriteArtifacts(read), canTrustArtifacts(read)]).toEqual([
      true,
      false,
      false,
    ]);
    expect([canReadArtifacts(write), canWriteArtifacts(write), canTrustArtifacts(write)]).toEqual([
      false,
      true,
      false,
    ]);
    // The point of the split: write does not imply trust, nor trust imply write.
    expect([canReadArtifacts(trust), canWriteArtifacts(trust), canTrustArtifacts(trust)]).toEqual([
      false,
      false,
      true,
    ]);
  });

  it('grants all to the super-authority and none to no claims', () => {
    const all = claimsWith('*');
    expect([canReadArtifacts(all), canWriteArtifacts(all), canTrustArtifacts(all)]).toEqual([
      true,
      true,
      true,
    ]);
    expect([canReadArtifacts(null), canWriteArtifacts(null), canTrustArtifacts(null)]).toEqual([
      false,
      false,
      false,
    ]);
  });

  it('does not match a look-alike authority', () => {
    expect(canTrustArtifacts(claimsWith('artifact:trusted', 'artifact'))).toBe(false);
  });
});
