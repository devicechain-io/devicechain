// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The artifact list under jsdom with the real i18n catalogs, the real API module and the real
// query documents. Faked: the GraphQL transport, the auth claims and the area-status read, so
// the criteria asserted are the criteria that would go on the wire and the authority gate
// under test is the real one.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, navigateMock, authorities, area } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  navigateMock: vi.fn(),
  authorities: { value: [] as string[] },
  area: { status: 'available' as 'available' | 'absent' | 'unknown' },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));
vi.mock('@/lib/capabilities', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/capabilities')>();
  return { ...actual, useAreaStatus: () => area.status };
});
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => navigateMock };
});
// The new-artifact drawer has its own tests; here it is only checked for being offered.
vi.mock('./NewArtifactForm', () => ({ NewArtifactForm: () => <div>new-artifact-form</div> }));

import ArtifactsPage from './ArtifactsPage';

const DIGEST = 'sha256:' + 'ab12cd34ef56'.padEnd(64, '0');

function artifact(over: Record<string, unknown> = {}) {
  return {
    id: 'id-1',
    createdAt: '2026-10-01T10:00:00Z',
    updatedAt: '2026-10-01T10:05:00Z',
    token: 'fw-1-2-0',
    kind: 'FIRMWARE',
    name: 'sensor-fw',
    version: '1.2.0',
    component: 'firmware',
    sizeBytes: String(5 * 1024 * 1024),
    digest: DIGEST,
    mediaType: 'application/octet-stream',
    compatibleProfiles: ['edge'],
    trustStatus: 'SIGNED',
    provenance: null,
    state: 'AVAILABLE',
    ...over,
  };
}

function respondWith(results: ReturnType<typeof artifact>[]) {
  gqlMock.mockImplementation(() =>
    Promise.resolve({
      artifacts: { results, pagination: { pageStart: 1, pageEnd: results.length, totalRecords: results.length } },
    }),
  );
}

function lastCriteria(): Record<string, unknown> {
  const call = gqlMock.mock.calls[gqlMock.mock.calls.length - 1];
  if (!call) throw new Error('no query was issued');
  return (call[2] as { criteria: Record<string, unknown> }).criteria;
}

function renderPage() {
  render(
    <MemoryRouter>
      <ArtifactsPage />
    </MemoryRouter>,
  );
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  navigateMock.mockReset();
  authorities.value = ['artifact:read'];
  area.status = 'available';
});

describe('ArtifactsPage states', () => {
  it('shows a loading state before the first answer', () => {
    gqlMock.mockImplementation(() => new Promise(() => {}));
    renderPage();
    expect(screen.getByText('Loading artifacts…')).toBeTruthy();
  });

  it('shows an empty state when the catalogue has no artifacts', async () => {
    respondWith([]);
    renderPage();
    expect(await screen.findByText(/No artifacts match/)).toBeTruthy();
  });

  it('shows the error rather than an empty list when the query fails', async () => {
    gqlMock.mockImplementation(() => Promise.reject(new Error('boom')));
    renderPage();
    expect(await screen.findByText('Failed to load data.')).toBeTruthy();
    expect(screen.queryByText(/No artifacts match/)).toBeNull();
  });
});

describe('ArtifactsPage rows', () => {
  it('shows trust, version, size and a shortened digest', async () => {
    respondWith([artifact()]);
    renderPage();

    const row = (await screen.findByText('sensor-fw')).closest('tr')!;
    expect(within(row).getByText('Signed')).toBeTruthy();
    expect(within(row).getByText('1.2.0')).toBeTruthy();
    expect(within(row).getByText('5.0 MiB')).toBeTruthy();
    // Shortened to the scheme plus 12 hex digits, not the whole 64.
    expect(within(row).getByText('sha256:ab12cd34ef56…')).toBeTruthy();
    expect(within(row).queryByText(DIGEST)).toBeNull();
  });

  it('marks an unverified artifact as such', async () => {
    respondWith([artifact({ trustStatus: 'UNVERIFIED' })]);
    renderPage();
    const row = (await screen.findByText('sensor-fw')).closest('tr')!;
    expect(within(row).getByText('Unverified')).toBeTruthy();
    expect(within(row).queryByText('Signed')).toBeNull();
  });

  it('opens the artifact on row click', async () => {
    respondWith([artifact()]);
    renderPage();
    fireEvent.click((await screen.findByText('sensor-fw')).closest('tr')!);
    expect(navigateMock).toHaveBeenCalledWith('/updates/artifacts/fw-1-2-0');
  });

  it('copying the digest does not also open the artifact', async () => {
    respondWith([artifact()]);
    renderPage();
    const row = (await screen.findByText('sensor-fw')).closest('tr')!;
    fireEvent.click(within(row).getByRole('button', { name: /sha256/ }));
    expect(navigateMock).not.toHaveBeenCalled();
  });
});

describe('ArtifactsPage filters', () => {
  it('sends no trust or state filter by default', async () => {
    respondWith([artifact()]);
    renderPage();
    await screen.findByText('sensor-fw');
    expect(lastCriteria()).toMatchObject({ pageNumber: 1, trustStatus: undefined, state: undefined });
  });

  it('re-queries with the chosen trust status', async () => {
    respondWith([artifact()]);
    renderPage();
    await screen.findByText('sensor-fw');

    fireEvent.click(screen.getByRole('button', { name: /Filter by trust status/ }));
    fireEvent.click(await screen.findByRole('button', { name: 'Unverified' }));

    await waitFor(() => expect(lastCriteria().trustStatus).toBe('UNVERIFIED'));
  });

  it('re-queries with the chosen lifecycle state', async () => {
    respondWith([artifact()]);
    renderPage();
    await screen.findByText('sensor-fw');

    fireEvent.click(screen.getByRole('button', { name: /Filter by lifecycle state/ }));
    fireEvent.click(await screen.findByRole('button', { name: 'Retired' }));

    await waitFor(() => expect(lastCriteria().state).toBe('RETIRED'));
  });

  it('narrows the loaded page by the search text, and says when nothing on the page matches', async () => {
    respondWith([artifact(), artifact({ id: 'id-2', token: 'cfg', name: 'site-config', version: '9' })]);
    renderPage();
    await screen.findByText('sensor-fw');

    fireEvent.change(screen.getByLabelText(/Filter the artifacts on this page/), { target: { value: 'site-config' } });
    await waitFor(() => expect(screen.queryByText('sensor-fw')).toBeNull());
    expect(screen.getByText('site-config')).toBeTruthy();

    fireEvent.change(screen.getByLabelText(/Filter the artifacts on this page/), { target: { value: 'zzz' } });
    expect(await screen.findByText(/Nothing on this page matches the filter/)).toBeTruthy();
  });
});

describe('ArtifactsPage authority', () => {
  it('hides New artifact from a reader', async () => {
    respondWith([artifact()]);
    renderPage();
    await screen.findByText('sensor-fw');
    expect(screen.queryByRole('button', { name: /New artifact/ })).toBeNull();
  });

  it('offers New artifact to a holder of artifact:write', async () => {
    authorities.value = ['artifact:read', 'artifact:write'];
    respondWith([artifact()]);
    renderPage();
    await screen.findByText('sensor-fw');

    fireEvent.click(screen.getByRole('button', { name: /New artifact/ }));
    expect(await screen.findByText('new-artifact-form')).toBeTruthy();
  });

  it('re-reads the catalogue when the drawer closes, so a just-declared artifact is listed', async () => {
    authorities.value = ['artifact:read', 'artifact:write'];
    respondWith([artifact()]);
    renderPage();
    await screen.findByText('sensor-fw');
    fireEvent.click(screen.getByRole('button', { name: /New artifact/ }));
    await screen.findByText('new-artifact-form');

    // Declared in the drawer (whose upload may or may not have finished), then closed.
    respondWith([artifact(), artifact({ id: 'id-2', token: 'gw-2-0', name: 'gateway-fw', state: 'UPLOADING' })]);
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(await screen.findByText('gateway-fw')).toBeTruthy();
  });

  it('does not offer New artifact while the area is not known to be deployed', async () => {
    authorities.value = ['artifact:read', 'artifact:write'];
    area.status = 'unknown';
    respondWith([artifact()]);
    renderPage();
    await screen.findByText('sensor-fw');
    expect(screen.queryByRole('button', { name: /New artifact/ })).toBeNull();
  });
});
