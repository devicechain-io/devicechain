// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The home page: registry counts, devices online and offline, and active alarms by severity,
// each section shown only to a member who may read what it counts. Only the transport is faked.
import '@/i18n/config';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ gql: vi.fn(), authorities: ['*'] as string[] }));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'ann', authorities: h.authorities, scopeAnchors: [] } }),
}));
vi.mock('@/auth/CurrentUserProvider', () => ({ useCurrentUser: () => ({ displayName: 'Ann' }) }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import Home from './Home';

const page = (total: number) => ({ results: [], pagination: { pageStart: 1, pageEnd: 0, totalRecords: total } });
const BY_SEVERITY: Record<string, number> = { CRITICAL: 3, MAJOR: 0, MINOR: 12, WARNING: 4, INDETERMINATE: 1 };

let sent: { op: string; variables: any }[] = [];
beforeEach(() => {
  sent = [];
  h.authorities = ['*'];
  h.gql.mockImplementation((_service: string, doc: unknown, variables: any) => {
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'Devices':
        return Promise.resolve({ devices: page(50) });
      case 'DeviceTypes':
        return Promise.resolve({ deviceTypes: page(6) });
      case 'DeviceStateCount':
        return Promise.resolve({ deviceStates: page(variables.criteria.active ? 41 : 7) });
      case 'Alarms':
        return Promise.resolve({ alarms: page(BY_SEVERITY[variables.criteria.severity]) });
      default:
        return Promise.resolve({});
    }
  });
});
afterEach(cleanup);

const renderHome = () =>
  render(
    <MemoryRouter>
      <Home />
    </MemoryRouter>,
  );
const cardCount = (label: string) => screen.getByText(label).previousElementSibling?.textContent;

describe('Home', () => {
  it('counts devices online and offline from the recorded state', async () => {
    renderHome();
    await waitFor(() => expect(cardCount('Devices online')).toBe('41'));
    expect(cardCount('Devices offline')).toBe('7');
    expect(sent.filter((s) => s.op === 'DeviceStateCount').map((s) => s.variables.criteria.active).sort()).toEqual([
      false,
      true,
    ]);
  });

  it('counts ACTIVE alarms for each severity, a zero included, each linking to the narrowed list', async () => {
    renderHome();
    const section = await screen.findByRole('region', { name: 'Active alarms by severity' });
    await waitFor(() => expect(within(section).getByText('12')).toBeTruthy());
    const link = (sev: string) => section.querySelector(`[data-shot="home-alarms-${sev}"]`) as HTMLAnchorElement;
    expect(link('critical').textContent).toContain('3');
    expect(link('major').textContent).toContain('0');
    expect(link('minor').getAttribute('href')).toBe('/alarms?state=ACTIVE&severity=MINOR');
    const asked = sent.filter((s) => s.op === 'Alarms').map((s) => s.variables.criteria);
    expect(asked.map((c) => c.severity).sort()).toEqual(['CRITICAL', 'INDETERMINATE', 'MAJOR', 'MINOR', 'WARNING']);
    expect(asked.every((c) => c.state === 'ACTIVE' && c.pageSize === 1)).toBe(true);
  });

  it('leaves out what the member may not read', async () => {
    h.authorities = ['device:read'];
    renderHome();
    await screen.findByText('Devices');
    expect(screen.queryByText('Devices online')).toBeNull();
    expect(screen.queryByRole('region', { name: 'Active alarms by severity' })).toBeNull();
    expect(sent.some((s) => s.op === 'DeviceStateCount' || s.op === 'Alarms')).toBe(false);
  });
});
