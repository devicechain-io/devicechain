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
import { cleanup, render, screen, within } from '@testing-library/react';
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
const PANEL_TITLE = 'Last known position';

// The panel now asks TWO services: device-management for whether this KIND of device
// reports position at all, and device-state for where it actually is. The double
// dispatches on the service name so a test can answer them independently — which is
// the whole point, since the interesting cases are the ones where the two disagree.
type Answers = { declared?: boolean | null; location?: unknown };

function respondWith({ declared = true, location = null }: Answers) {
  gqlMock.mockImplementation((service: string) => {
    if (service === 'device-management') {
      return Promise.resolve({
        deviceLocationDeclaration:
          declared == null ? null : { declared, declaration: declared ? {} : null },
      });
    }
    return Promise.resolve({ latestLocation: location });
  });
}

// callsTo counts the queries actually sent to one service, so "costs no round-trip"
// stays assertable now that a second, unrelated query is always in flight.
function callsTo(service: string): number {
  return gqlMock.mock.calls.filter((call) => call[0] === service).length;
}

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
    respondWith({ location: FULL_FIX });

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
    respondWith({
      location: {
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
    respondWith({});

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(NO_PERMISSION)).toBeTruthy();
    // Not the empty state, which says something false about the device…
    expect(screen.queryByText(NEVER_LOCATED)).toBeNull();
    // …and the known-absent authority costs no POSITION round-trip at all. The
    // declaration query still runs: it is device:read, which this viewer holds, and
    // it is what decides whether the panel belongs on the page in the first place.
    expect(callsTo('device-state')).toBe(0);
  });

  // The claim is a snapshot: a token minted before a role change still advertises
  // an authority the server has since withdrawn. The refusal path has to land in
  // the same state, or a stale token turns a permission boundary into an error.
  it('lands in the same state when a stale claim is refused by the server', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    gqlMock.mockImplementation((service: string) => {
      if (service === 'device-management') {
        return Promise.resolve({ deviceLocationDeclaration: { declared: true, declaration: {} } });
      }
      return Promise.reject(
        new GraphQLRequestError('forbidden: missing required authority', 200, [
          { message: 'forbidden: missing required authority' },
        ]),
      );
    });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(NO_PERMISSION)).toBeTruthy();
    expect(callsTo('device-state')).toBeGreaterThan(0);
    // The server's raw refusal text is never what the operator reads.
    expect(screen.queryByText('forbidden: missing required authority')).toBeNull();
  });

  it('says the device has no position only when the server says so', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    respondWith({ declared: true, location: null });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(NEVER_LOCATED)).toBeTruthy();
    expect(screen.queryByText(NO_PERMISSION)).toBeNull();
  });

  // The counterweight to the refusal classification: a broken service must still
  // look broken. Swallowing every failure into the permission state would hide an
  // outage behind a sentence that reads like normal operation.
  it('still surfaces a non-authorization failure as an error', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    gqlMock.mockImplementation((service: string) => {
      if (service === 'device-management') {
        return Promise.resolve({ deviceLocationDeclaration: { declared: true, declaration: {} } });
      }
      return Promise.reject(new GraphQLRequestError('Request failed (503)', 503));
    });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText('Request failed (503)')).toBeTruthy();
    expect(screen.queryByText(NO_PERMISSION)).toBeNull();
    expect(screen.queryByText(NEVER_LOCATED)).toBeNull();
  });

  // ── The hide rule ────────────────────────────────────────────────────────
  //
  // The panel appears only for devices whose profile says they report position —
  // OR which have actually reported one. The three tests below are the corners of
  // that rule, and the third is the one that keeps it honest.

  it('shows the panel for a device whose profile declares position', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    respondWith({ declared: true, location: FULL_FIX });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(PANEL_TITLE)).toBeTruthy();
    expect(valueFor('Latitude')).toBe('34.052235');
  });

  // 🔴 The awkward shape of this test is the point, and it is worth explaining
  // because the obvious version CANNOT FAIL.
  //
  // "Assert nothing rendered" is only meaningful once the component has had its
  // answers; before that, nothing has rendered either — so `waitFor(container is
  // empty)` succeeds on its very first check, at the moment of mount, and passes
  // just as happily for a panel that never hides anything. (It did: a mutation
  // that removed the hide rule entirely left it green.)
  //
  // So the settle signal is a CONTROL panel rendered beside it, for a device that
  // DOES declare position. Both mount in the same commit and are answered from the
  // same already-resolved promises, so when the control has painted its heading the
  // tree has settled — and the assertion becomes a comparison between two panels
  // rather than a claim about an empty page: exactly ONE heading, not two.
  it('renders no panel at all for a device that declares no position and has none', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    // Answers dispatch on the DEVICE TOKEN as well as the service, so the control
    // and the subject can disagree inside one render.
    gqlMock.mockImplementation((service: string, _doc: unknown, vars: { deviceToken: string }) => {
      const declares = vars.deviceToken === 'tracker-1';
      if (service === 'device-management') {
        return Promise.resolve({
          deviceLocationDeclaration: { declared: declares, declaration: declares ? {} : null },
        });
      }
      return Promise.resolve({ latestLocation: declares ? FULL_FIX : null });
    });

    render(
      <div>
        <div data-testid="control">
          <DeviceLocationPanel deviceToken="tracker-1" />
        </div>
        <div data-testid="subject">
          <DeviceLocationPanel deviceToken="excavator-7" />
        </div>
      </div>,
    );

    // The control painting is the proof that both panels have their answers. It is
    // waited for INSIDE its own slot: an unscoped findByText would blow up on
    // "found multiple elements" the moment the rule broke, reporting a query
    // problem where the real finding is the count asserted on the next line.
    expect(await within(screen.getByTestId('control')).findByText(PANEL_TITLE)).toBeTruthy();
    expect(screen.getAllByText(PANEL_TITLE)).toHaveLength(1);
    expect(screen.getByTestId('subject').textContent).toBe('');
    // Not "no position recorded" either. The device does not report position at
    // all, so an empty panel would be an answer to a question nobody asked.
    expect(screen.queryByText(NEVER_LOCATED)).toBeNull();
  });

  // 🔴🔴 THE ONE THAT STOPS THIS HIDING REAL DATA. The declaration is DESCRIPTIVE:
  // ingest never gates on it, so a device can report a position long before anyone
  // edits its profile to say it would. Hiding on the declaration alone would make
  // the console silently withhold stored, correct fixes on the strength of an
  // unedited profile — a UI that tells an operator a working device has no position.
  it('still shows a stored position for a device whose profile declares none', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    respondWith({ declared: false, location: FULL_FIX });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(PANEL_TITLE)).toBeTruthy();
    expect(valueFor('Latitude')).toBe('34.052235');
    expect(valueFor('Longitude')).toBe('-118.243683');
  });

  // A refusal is not evidence of absence. An undeclared device read by someone
  // without location:read must NOT be hidden: the server said "you may not look",
  // not "there is nothing there", and hiding would let a permission boundary decide
  // what exists.
  it('does not hide an undeclared device when the position read was refused', async () => {
    authState.claims = claimsWith(VIEWER);
    respondWith({ declared: false });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(NO_PERMISSION)).toBeTruthy();
  });

  // Only a POSITIVE "declares nothing" hides. A null capability — the token
  // resolved to no device — is not that answer; it says nothing about position at
  // all, and treating "unknown" as "no" would let an unrelated lookup miss decide
  // what the operator is allowed to see.
  it('does not hide when the declaration is unknown rather than absent', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    respondWith({ declared: null, location: null });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(PANEL_TITLE)).toBeTruthy();
    expect(screen.queryByText(NEVER_LOCATED)).toBeTruthy();
  });

  // The declaration lookup only ever decides whether to HIDE, so its failure must
  // degrade to showing the position — never to an error page over a position that
  // loaded perfectly well.
  it('shows the position when the declaration lookup itself fails', async () => {
    authState.claims = claimsWith(VIEWER_PLUS_LOCATION);
    gqlMock.mockImplementation((service: string) => {
      if (service === 'device-management') {
        return Promise.reject(new GraphQLRequestError('Request failed (503)', 503));
      }
      return Promise.resolve({ latestLocation: FULL_FIX });
    });

    render(<DeviceLocationPanel deviceToken="excavator-7" />);

    expect(await screen.findByText(PANEL_TITLE)).toBeTruthy();
    expect(valueFor('Latitude')).toBe('34.052235');
  });
});
