// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { getOperationAST, parse } from 'graphql';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import type { FacetKey } from '@/lib/api/facet-keys';

const transport = vi.hoisted(() => ({ gql: vi.fn(), setFacetKey: vi.fn() }));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'ann', authorities: ['*'], scopeAnchors: [] } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: transport.gql };
});

import FacetKeysPage from './FacetKeysPage';

const declaration: FacetKey = {
  id: 'climate-id', memberType: 'area', key: 'climate', valueType: 'STRING',
  source: 'user', values: ['temperate'], label: 'Climate',
};
let pages: FacetKey[][];
let failLookup: boolean;
let pendingLookup: Promise<FacetKey[]> | null;
const lookupPages = vi.fn();

beforeEach(() => {
  vi.clearAllMocks();
  pages = [[declaration]];
  failLookup = false;
  pendingLookup = null;
  transport.setFacetKey.mockResolvedValue({ setFacetKey: { memberType: 'area', key: 'climate' } });
  transport.gql.mockImplementation(async (_service: string, document: { toString(): string }, variables: {
    criteria?: { memberType: string | null; pageNumber: number; pageSize: number };
    request?: unknown;
  }) => {
    const operation = getOperationAST(parse(document.toString()))?.name?.value;
    if (operation === 'SetFacetKey') return transport.setFacetKey(variables.request);
    if (operation !== 'FacetKeys' || !variables.criteria) throw new Error('Unexpected operation');
    const { memberType, pageNumber, pageSize } = variables.criteria;
    if (memberType === null) {
      return { facetKeys: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } } };
    }
    lookupPages(memberType, pageNumber, pageSize);
    if (failLookup) throw new Error('Lookup unavailable');
    const results = pendingLookup ? await pendingLookup : memberType === 'area' ? pages[pageNumber - 1] ?? [] : [];
    return { facetKeys: { results, pagination: {
      pageStart: (pageNumber - 1) * pageSize + 1,
      pageEnd: (pageNumber - 1) * pageSize + results.length,
      totalRecords: memberType === 'area' ? pages.flat().length : 0,
    } } };
  });
});
afterEach(cleanup);

function choose(field: string, option: string) {
  fireEvent.click(screen.getByLabelText(field));
  fireEvent.click(screen.getByRole('button', { name: option }));
}

function openForm(key = 'climate') {
  render(<MemoryRouter><ConfirmProvider><FacetKeysPage /></ConfirmProvider></MemoryRouter>);
  fireEvent.click(screen.getByRole('button', { name: 'New facet' }));
  choose('Member family', 'Area');
  fireEvent.change(screen.getByLabelText('Attribute key'), { target: { value: key } });
  choose('Value type', 'LONG');
}

const warning = () => screen.queryByText(/Changing the declaration from STRING to LONG/);

async function checked() {
  await waitFor(() => expect(lookupPages).toHaveBeenCalledWith('area', 1, 500));
  await act(async () => {});
}

describe('Facet declaration type warning', () => {
  it('warns before Save without mutating, then saves the requested type', async () => {
    openForm('  climate  ');
    const message = await screen.findByText(/Changing the declaration from STRING to LONG/);
    expect(message.textContent).toContain('retains existing stored values');
    expect(message.textContent).toContain('may no longer match the declared axis');
    expect(transport.setFacetKey).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Save facet' }));
    await waitFor(() => expect(transport.setFacetKey).toHaveBeenCalledWith({
      memberType: 'area', key: 'climate', valueType: 'LONG', values: null, label: null,
    }));
  });

  it('removes the warning when returning to the declared type without refetching', async () => {
    openForm();
    await screen.findByText(/Changing the declaration from STRING to LONG/);
    const calls = lookupPages.mock.calls.length;
    choose('Value type', 'STRING');
    expect(warning()).toBeNull();
    expect(screen.queryByText(/Changing the declaration/)).toBeNull();
    expect(lookupPages).toHaveBeenCalledTimes(calls);
    expect(transport.setFacetKey).not.toHaveBeenCalled();
  });

  it('does not warn for a new key', async () => {
    openForm('new-key');
    await checked();
    expect(warning()).toBeNull();
    expect(screen.queryByText(/could not be checked/)).toBeNull();
    expect(transport.setFacetKey).not.toHaveBeenCalled();
  });

  it('finds an existing declaration on the second page', async () => {
    pages = [Array.from({ length: 500 }, (_, i) => ({
      ...declaration, id: `other-${i}`, key: `other-${i}`,
    })), [declaration]];
    openForm();
    await screen.findByText(/Changing the declaration from STRING to LONG/);
    expect(lookupPages).toHaveBeenCalledWith('area', 1, 500);
    expect(lookupPages).toHaveBeenCalledWith('area', 2, 500);
    expect(transport.setFacetKey).not.toHaveBeenCalled();
  });

  it('shows a nonblocking failure message when the impact cannot be checked', async () => {
    failLookup = true;
    openForm();
    await screen.findByText(/The impact of this declaration could not be checked/);
    expect(warning()).toBeNull();
    expect(transport.setFacetKey).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Save facet' }));
    await waitFor(() => expect(transport.setFacetKey).toHaveBeenCalledWith(
      expect.objectContaining({ valueType: 'LONG' }),
    ));
  });

  it('ignores stale responses and shows no warning while checking', async () => {
    let resolve!: (facets: FacetKey[]) => void;
    pendingLookup = new Promise((done) => { resolve = done; });
    openForm();
    expect(warning()).toBeNull();
    expect(screen.queryByText(/could not be checked/)).toBeNull();
    await waitFor(() => expect(lookupPages).toHaveBeenCalledWith('area', 1, 500));
    pendingLookup = null;
    fireEvent.change(screen.getByLabelText('Attribute key'), { target: { value: 'new-key' } });
    await checked();
    await act(async () => { resolve([declaration]); });
    expect(warning()).toBeNull();
    expect(transport.setFacetKey).not.toHaveBeenCalled();
  });
});
