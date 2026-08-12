// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, cleanup } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { AreaCapabilityProvider, useAreaStatus } from '@/lib/capabilities';
import { AreaGate } from '@/components/AreaUnavailable';

const listFunctionalAreas = vi.fn();
const listAdminFunctionalAreas = vi.fn();
const auth = {
  isAuthenticated: true,
  isIdentityAuthenticated: false,
  isLoading: false,
};

vi.mock('@/lib/api/user-management', () => ({
  listFunctionalAreas: (...a: unknown[]) => listFunctionalAreas(...a),
}));
vi.mock('@/lib/api/admin', () => ({
  listAdminFunctionalAreas: (...a: unknown[]) => listAdminFunctionalAreas(...a),
}));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => auth }));

function Harness({ children }: { children: React.ReactNode }) {
  return (
    <MemoryRouter>
      <AreaCapabilityProvider>{children}</AreaCapabilityProvider>
    </MemoryRouter>
  );
}

function Probe({ area }: { area: 'outbound-connectors' | 'device-management' }) {
  return <span data-testid="status">{useAreaStatus(area)}</span>;
}

// This workspace does not enable vitest globals, so Testing Library's automatic
// cleanup never runs — without it each render leaks its DOM into the next test and
// a query matches an element from a previous case.
afterEach(cleanup);

beforeEach(() => {
  listFunctionalAreas.mockReset();
  listAdminFunctionalAreas.mockReset();
  auth.isAuthenticated = true;
  auth.isIdentityAuthenticated = false;
  auth.isLoading = false;
});

describe('area status', () => {
  it('reports an area the instance deployed as available', async () => {
    listFunctionalAreas.mockResolvedValue(['device-management', 'user-management']);
    render(
      <Harness>
        <Probe area="device-management" />
      </Harness>,
    );
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('available'));
  });

  it('reports an area the instance did not deploy as absent', async () => {
    listFunctionalAreas.mockResolvedValue(['device-management', 'user-management']);
    render(
      <Harness>
        <Probe area="outbound-connectors" />
      </Harness>,
    );
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('absent'));
  });
});

describe('AreaGate', () => {
  it('renders the surface when its area is deployed', async () => {
    listFunctionalAreas.mockResolvedValue(['outbound-connectors', 'user-management']);
    render(
      <Harness>
        <AreaGate area="outbound-connectors">
          <p>the real page</p>
        </AreaGate>
      </Harness>,
    );
    await waitFor(() => expect(screen.getByText('the real page')).toBeTruthy());
    expect(screen.queryByTestId('area-unavailable')).toBeNull();
  });

  // The counterweight to the test above, and the one that matters: gating that
  // hides a DEPLOYED feature is a worse bug than the 405 this replaces.
  it('replaces the surface with an explanation when its area is absent', async () => {
    listFunctionalAreas.mockResolvedValue(['user-management']);
    render(
      <Harness>
        <AreaGate area="outbound-connectors">
          <p>the real page</p>
        </AreaGate>
      </Harness>,
    );
    await waitFor(() => expect(screen.getByTestId('area-unavailable')).toBeTruthy());
    expect(screen.queryByText('the real page')).toBeNull();
  });

  // I first wrote this asserting the gated child NEVER renders, because that is
  // what AreaGate's comment claimed. It failed: fail-open means 'unknown' renders
  // the child, so on a cold load it mounts once before the answer arrives. The
  // comment was wrong, not the code — this now pins the real contract, which is
  // that the child is dropped once the answer is known and does not come back.
  it('drops the gated child once the answer arrives, and does not remount it', async () => {
    const rendered = vi.fn();
    function Expensive() {
      rendered();
      return <p>the real page</p>;
    }
    listFunctionalAreas.mockResolvedValue(['user-management']);
    render(
      <Harness>
        <AreaGate area="outbound-connectors">
          <Expensive />
        </AreaGate>
      </Harness>,
    );
    await waitFor(() => expect(screen.getByTestId('area-unavailable')).toBeTruthy());

    const afterSettle = rendered.mock.calls.length;
    await new Promise((r) => setTimeout(r, 20));
    expect(rendered.mock.calls.length).toBe(afterSettle);
    expect(screen.queryByText('the real page')).toBeNull();
  });

  it('renders the surface while the answer is still unknown', () => {
    // Never resolves: the fail-open contract has to hold DURING the fetch too, or
    // every gated surface would flash an unavailable state on each page load.
    listFunctionalAreas.mockReturnValue(new Promise(() => {}));
    render(
      <Harness>
        <AreaGate area="outbound-connectors">
          <p>the real page</p>
        </AreaGate>
      </Harness>,
    );
    expect(screen.getByText('the real page')).toBeTruthy();
    expect(screen.queryByTestId('area-unavailable')).toBeNull();
  });
});

describe('when discovery fails', () => {
  let warn: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
  });
  afterEach(() => warn.mockRestore());

  it('leaves the surface visible — fail OPEN, not closed', async () => {
    listFunctionalAreas.mockRejectedValue(new Error('network'));
    render(
      <Harness>
        <AreaGate area="outbound-connectors">
          <p>the real page</p>
        </AreaGate>
      </Harness>,
    );
    await waitFor(() => expect(listFunctionalAreas).toHaveBeenCalled());
    expect(screen.getByText('the real page')).toBeTruthy();
    expect(screen.queryByTestId('area-unavailable')).toBeNull();
  });

  // 🔴 Without this, the whole mechanism could rot into a permanent no-op and
  // every other test here would still pass: "gating is broken" and "everything is
  // deployed" render identically. The warning is the only thing that tells them
  // apart, which makes asserting it the difference between a control and a
  // control that cannot fail.
  it('says so, rather than failing open silently', async () => {
    listFunctionalAreas.mockRejectedValue(new Error('network'));
    render(
      <Harness>
        <AreaGate area="outbound-connectors">
          <p>the real page</p>
        </AreaGate>
      </Harness>,
    );
    await waitFor(() => expect(warn).toHaveBeenCalled());
    expect(String(warn.mock.calls[0][0])).toContain('functional areas');
  });
});

describe('an implausible response', () => {
  let warn: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
  });
  afterEach(() => warn.mockRestore());

  // The catastrophic direction. Trusting a nonsense response marks EVERY gated
  // area absent, i.e. hides working features across the console — strictly worse
  // than the 405 this replaces. user-management served the answer, so a reply
  // omitting it cannot be a real area set.
  it('is ignored rather than hiding every gated surface', async () => {
    listFunctionalAreas.mockResolvedValue([]);
    render(
      <Harness>
        <AreaGate area="outbound-connectors">
          <p>the real page</p>
        </AreaGate>
      </Harness>,
    );
    await waitFor(() => expect(warn).toHaveBeenCalled());
    expect(screen.getByText('the real page')).toBeTruthy();
    expect(screen.queryByTestId('area-unavailable')).toBeNull();
  });

  // The counterweight: a small-but-genuine instance must still gate. Without
  // this, the guard above could be "ignore everything" and both tests would pass.
  it('still gates a genuine minimal instance', async () => {
    listFunctionalAreas.mockResolvedValue(['user-management', 'device-management']);
    render(
      <Harness>
        <AreaGate area="outbound-connectors">
          <p>the real page</p>
        </AreaGate>
      </Harness>,
    );
    await waitFor(() => expect(screen.getByTestId('area-unavailable')).toBeTruthy());
    expect(warn).not.toHaveBeenCalled();
  });
});

describe('lane selection', () => {
  it('falls back to the tenant plane when the identity lane refuses', async () => {
    // An expired identity token must not forfeit gating for a caller who still
    // holds a good tenant session — collapsing "this lane said no" into
    // "discovery failed" is what makes the phantom feature come back
    // intermittently, which is worse than never having gated at all.
    auth.isIdentityAuthenticated = true;
    listAdminFunctionalAreas.mockRejectedValue(new Error('401'));
    listFunctionalAreas.mockResolvedValue(['device-management', 'user-management']);

    render(
      <Harness>
        <Probe area="device-management" />
      </Harness>,
    );

    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('available'));
    expect(listAdminFunctionalAreas).toHaveBeenCalled();
    expect(listFunctionalAreas).toHaveBeenCalled();
  });

  it('asks nothing at all when no session is held', () => {
    auth.isAuthenticated = false;
    auth.isIdentityAuthenticated = false;
    render(
      <Harness>
        <Probe area="device-management" />
      </Harness>,
    );
    expect(listFunctionalAreas).not.toHaveBeenCalled();
    expect(listAdminFunctionalAreas).not.toHaveBeenCalled();
    expect(screen.getByTestId('status').textContent).toBe('unknown');
  });
});
