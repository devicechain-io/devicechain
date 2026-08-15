// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts). The real i18n catalogs are wired in
// by importing the config for its side effect, so every assertion is on WHAT AN OPERATOR
// SEES rather than on a translation key or a prop.
//
// One seam is faked: the GraphQL transport (`gql`). The real API module, the real query
// document and the real page render — so the criteria asserted below are the criteria
// that would go on the wire.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, navigateMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), navigateMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// Navigation is the row's only observable effect, so it is the seam the link assertion
// reads. Everything else about the router stays real.
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => navigateMock };
});

import CommandBatchesPage from './CommandBatchesPage';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  navigateMock.mockReset();
});

function batch(over: Partial<Record<string, unknown>> = {}) {
  return {
    id: 'id-1',
    token: 'batch-1',
    createdAt: '2026-08-12T12:00:00Z',
    name: 'reboot',
    targetKind: 'DEVICE_LIST',
    groupToken: null,
    groupVersion: null,
    allowPartial: true,
    resolved: 10,
    accepted: 7,
    cancelledAt: null,
    cancelledCount: null,
    ...over,
  };
}

function respondWith(results: ReturnType<typeof batch>[]) {
  gqlMock.mockImplementation(() =>
    Promise.resolve({
      commandBatches: {
        results,
        pagination: { pageStart: 1, pageEnd: results.length, totalRecords: results.length },
      },
    }),
  );
}

function renderPage() {
  render(
    <MemoryRouter>
      <CommandBatchesPage />
    </MemoryRouter>,
  );
}

/** The criteria object of the most recent commandBatches call. */
function lastCriteria(): Record<string, unknown> {
  // Indexed rather than .at(-1): the console's tsconfig lib predates ES2022, so `.at`
  // type-checks nowhere here even though vitest transpiles it happily.
  const call = gqlMock.mock.calls[gqlMock.mock.calls.length - 1];
  if (!call) throw new Error('no query was issued');
  return (call[2] as { criteria: Record<string, unknown> }).criteria;
}

/** The rendered row for a batch, once the list has painted. */
async function rowFor(command: string): Promise<HTMLElement> {
  const row = (await screen.findByText(command)).closest('tr');
  if (!row) throw new Error(`no row rendered for ${command}`);
  return row;
}

describe('CommandBatchesPage rows', () => {
  // 🔴 REFUSED IS DERIVED, NOT STORED. The record carries `resolved` and `accepted`; the
  // number that matters to an operator — how many devices got nothing — is the gap
  // between them, and it is the whole reason the column exists.
  it('shows accepted of resolved, and the refused remainder', async () => {
    respondWith([batch({ resolved: 5000, accepted: 4900 })]);

    renderPage();

    const row = await rowFor('reboot');
    expect(within(row).getByText('4900 of 5000')).toBeTruthy();
    expect(within(row).getByText('100')).toBeTruthy();
  });

  it('names the target kind, and the group and version when it was fired at one', async () => {
    respondWith([
      batch({ name: 'setpoint', targetKind: 'GROUP', groupToken: 'north-hvac', groupVersion: 3 }),
    ]);

    renderPage();

    const row = await rowFor('setpoint');
    expect(within(row).getByText('Group')).toBeTruthy();
    // The FROZEN version the target set was resolved against — the thing that lets an
    // audit answer what the group meant when this fired, after someone edits it.
    expect(within(row).getByText('north-hvac @ v3')).toBeTruthy();
  });

  // The badge is a fact about the batch, not decoration: an operator scanning the list
  // must be able to see which fleet writes were called off without opening each one.
  it('badges a cancelled batch, and only a cancelled one', async () => {
    respondWith([
      batch({ id: 'id-1', token: 'b-live', name: 'live-one' }),
      batch({
        id: 'id-2',
        token: 'b-off',
        name: 'called-off',
        cancelledAt: '2026-08-12T13:00:00Z',
        cancelledCount: 42,
      }),
    ]);

    renderPage();

    expect(within(await rowFor('called-off')).getByText('Cancelled')).toBeTruthy();
    expect(within(await rowFor('live-one')).queryByText('Cancelled')).toBeNull();
  });

  it('opens the batch when its row is activated', async () => {
    respondWith([batch({ token: 'batch-77' })]);

    renderPage();

    fireEvent.click(await rowFor('reboot'));
    expect(navigateMock).toHaveBeenCalledWith('/command-batches/batch-77');
  });
});

describe('CommandBatchesPage filters', () => {
  // 🔴🔴 THE ONE THAT MATTERS. A filter that renders but never reaches the criteria is
  // worse than no filter: the operator types a command name, the list narrows to nothing
  // it actually narrowed, and they read the unfiltered page as the answer. Asserted on
  // the criteria that go on the wire, since the server does the filtering.
  it('sends the typed command name as criteria', async () => {
    respondWith([batch()]);

    renderPage();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText('Filter by command'), { target: { value: 'reboot' } });

    // The text filters are debounced, so the assertion waits for the settled request.
    await waitFor(() => expect(lastCriteria().name).toBe('reboot'));
    expect(lastCriteria().pageNumber).toBe(1);
  });

  it('sends the typed group token as criteria', async () => {
    respondWith([batch()]);

    renderPage();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText('Filter by group'), { target: { value: 'north-hvac' } });

    await waitFor(() => expect(lastCriteria().groupToken).toBe('north-hvac'));
  });

  // The counterweight to the two above: an unset filter must be OMITTED, not sent as an
  // empty string. `targetKind: ''` is an unrecognized value, and the service matches
  // nothing for one — so a page that sent its empty filters would show an empty list and
  // blame the tenant for having no batches.
  it('omits a filter nobody set', async () => {
    respondWith([batch()]);

    renderPage();

    await waitFor(() => expect(gqlMock).toHaveBeenCalled());
    const criteria = lastCriteria();
    expect(criteria.name).toBeUndefined();
    expect(criteria.groupToken).toBeUndefined();
    expect(criteria.targetKind).toBeUndefined();
    expect(criteria.pageSize).toBe(20);
  });
});

describe('CommandBatchesPage create action', () => {
  // The list is also the way IN to firing a fleet write, and the drawer is mounted by the
  // action rather than rendered permanently — so nothing about the create form is on the
  // page until this button is pressed. The form's own behaviour is pinned in
  // CreateBatchForm.test.tsx; what this asserts is the WIRING, which no test there can
  // see: a drawer nobody can open is indistinguishable from a feature that never shipped.
  it('opens the create drawer from the primary action', async () => {
    // The drawer's own reads (devices, groups) go to different documents; answering them
    // with the list's shape would throw inside the API module rather than the component.
    gqlMock.mockImplementation((_service: string, document: unknown) => {
      const doc = String(document);
      if (doc.includes('query CommandBatches')) {
        return Promise.resolve({
          commandBatches: {
            results: [batch()],
            pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
          },
        });
      }
      if (doc.includes('query Devices(')) {
        return Promise.resolve({
          devices: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } },
        });
      }
      return Promise.resolve({ entityGroups: { results: [] } });
    });

    renderPage();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    // Nothing of the form exists before the press.
    expect(screen.queryByText('If some devices cannot receive it')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: /New batch/ }));

    expect(await screen.findByText('If some devices cannot receive it')).toBeTruthy();
  });
});
