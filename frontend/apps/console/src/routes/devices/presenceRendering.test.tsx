// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 WHY THIS EXISTS, beside lib/presence.test.ts. That file pins the CLASSIFIER; this
// one pins that the screens are actually WIRED to it. The two failures are different:
// a classifier that answers correctly while the list still passes it `active` alone
// leaves every inferred device reading "Disconnected", and every assertion in the unit
// table still passes. So the assertions here are on the strings an operator sees, with
// the real i18n catalogs loaded and only the GraphQL transport faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, navigateMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), navigateMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => navigateMock, useParams: () => ({ token: 'dev-1' }) };
});
// The detail page gates its credentials tab on an authority; presence is not part of
// that decision, so the claims here are the read-only viewer baseline and nothing else.
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    claims: {
      tenant: 'acme',
      username: 'operator@acme.test',
      roles: [],
      authorities: ['device:read', 'state:read'],
      typ: 'access',
    },
  }),
}));

import DevicesPage from './DevicesPage';
import DeviceDetailPage from './DeviceDetailPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  navigateMock.mockReset();
});

function device(token: string) {
  return {
    id: `id-${token}`,
    token,
    name: token,
    description: '',
    externalId: null,
    metadata: null,
    createdAt: '2026-08-12T12:00:00Z',
    deviceType: {
      id: 'dt-1',
      token: 'sensor',
      name: 'Sensor',
      icon: 'cpu',
      backgroundColor: '#000000',
      foregroundColor: '#ffffff',
      borderColor: '#000000',
    },
  };
}

function state(deviceToken: string, active: boolean, presenceSource: string) {
  return {
    id: `st-${deviceToken}`,
    deviceToken,
    active,
    lastConnectTime: '2026-08-12T12:00:00Z',
    lastDisconnectTime: active ? null : '2026-08-12T13:00:00Z',
    lastActivityTime: '2026-08-12T12:30:00Z',
    inactivityTimeout: 600,
    presenceSource,
  };
}

// One double for both services, dispatching on the service name — the list page asks
// device-management for devices and device-state for their presence in one render.
function respond(devices: ReturnType<typeof device>[], states: ReturnType<typeof state>[]) {
  gqlMock.mockImplementation((service: string) => {
    if (service === 'device-management') {
      return Promise.resolve({
        devices: {
          results: devices,
          pagination: { pageStart: 1, pageEnd: devices.length, totalRecords: devices.length },
        },
      });
    }
    if (service === 'device-state') {
      return Promise.resolve({ deviceStatesByDeviceToken: states });
    }
    return Promise.resolve({});
  });
}

describe('the device list', () => {
  it('gives the three presence cases three different words', async () => {
    respond(
      [device('up'), device('quiet'), device('dead')],
      [
        state('up', true, 'INFERRED'),
        state('quiet', false, 'INFERRED'),
        state('dead', false, 'ASSERTED'),
      ],
    );
    render(
      <MemoryRouter>
        <DevicesPage />
      </MemoryRouter>,
    );

    // Asserted PER ROW, not as three strings loose on the page: handing StatusDot
    // `active` instead of the whole state still renders all three words somewhere on a
    // list that happens to contain all three cases, and only the row-scoped assertion
    // notices which device got which.
    // Presence loads in a SECOND round-trip, after the rows are already on screen, so
    // wait for it before reading the column — otherwise every row still says "—" and
    // the assertions would be measuring the loading state.
    await screen.findByText('Disconnected');
    const rowFor = (token: string) => {
      // The token appears twice in its row (name cell + mono token), so take the row
      // rather than the element.
      return screen.getAllByText(token)[0].closest('tr') as HTMLElement;
    };
    expect(within(rowFor('up')).getByText('Online')).toBeTruthy();
    expect(within(rowFor('quiet')).getByText('Offline')).toBeTruthy();
    expect(within(rowFor('dead')).getByText('Disconnected')).toBeTruthy();
  });

  it('renders a device with no state row as a neutral dash, not as a presence claim', async () => {
    respond([device('never-reported')], []);
    render(
      <MemoryRouter>
        <DevicesPage />
      </MemoryRouter>,
    );

    const cells = await screen.findAllByText('never-reported');
    const row = cells[0].closest('tr') as HTMLElement;
    expect(within(row).getAllByText('—').length).toBeGreaterThan(0);
    expect(within(row).queryByText('Offline')).toBeNull();
    expect(within(row).queryByText('Disconnected')).toBeNull();
    expect(within(row).queryByText('Online')).toBeNull();
  });
});

// The detail page's connectivity tab. It renders the whole page and reads the panel,
// because DeviceStatePanel is deliberately private to that file.
async function renderDetail(presenceSource: string, active: boolean) {
  gqlMock.mockImplementation((service: string) => {
    if (service === 'device-management') {
      return Promise.resolve({ devicesByToken: [device('dev-1')], deviceTypes: { results: [], pagination: {} } });
    }
    if (service === 'device-state') {
      return Promise.resolve({
        deviceStatesByDeviceToken: [state('dev-1', active, presenceSource)],
        latestMeasurements: [],
        latestLocation: null,
      });
    }
    return Promise.resolve({ events: { results: [], pagination: {} } });
  });
  render(
    <MemoryRouter>
      <ConfirmProvider>
        <DeviceDetailPage />
      </ConfirmProvider>
    </MemoryRouter>,
  );
  // Connectivity is not the default tab, and Radix mounts only the active one. It
  // switches on mouseDown, not click — a plain click leaves the basic tab showing.
  fireEvent.mouseDown(await screen.findByRole('tab', { name: 'Connectivity' }));
  await waitFor(() => expect(screen.getAllByText('Presence source').length).toBeGreaterThan(0));
}

const CALLOUT = 'No recent activity';

describe('the device detail connectivity panel', () => {
  it('says Disconnected only when a transport reported it', async () => {
    await renderDetail('ASSERTED', false);
    expect(screen.getByText('Disconnected')).toBeTruthy();
    expect(screen.getByText('Reported by the transport')).toBeTruthy();
  });

  it('keeps the shipped Offline wording for silence, and explains it', async () => {
    await renderDetail('INFERRED', false);
    expect(screen.getByText('Offline')).toBeTruthy();
    expect(screen.getByText('Inferred from activity')).toBeTruthy();
    expect(screen.getByText(CALLOUT)).toBeTruthy();
  });

  it('shows an unrecognised presence source verbatim and refuses the confident word', async () => {
    // The fail-safe, at the surface: a value this console has never heard of must not
    // be worded as one of the two it knows, and must not read as a reported death.
    await renderDetail('SOMETHING-NEW', false);
    expect(screen.getByText('SOMETHING-NEW')).toBeTruthy();
    expect(screen.queryByText('Disconnected')).toBeNull();
    expect(screen.getByText(CALLOUT)).toBeTruthy();
  });

  // 🔴 The callout is a claim about EVIDENCE, so it must appear in exactly one of the
  // four combinations. Dropping either conjunct from its guard shows it to an operator
  // whose device is fine, or hides it from the one case it was written for — and both
  // mutants survive a test that only checks the quiet case.
  it.each([
    ['asserted and active', 'ASSERTED', true],
    ['inferred and active', 'INFERRED', true],
    ['asserted and inactive', 'ASSERTED', false],
  ] as const)('does not show the callout when %s', async (_name, source, active) => {
    await renderDetail(source, active);
    expect(screen.queryByText(CALLOUT)).toBeNull();
  });
});
