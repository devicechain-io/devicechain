// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom. The real i18n catalogs are wired in by importing the config for
// its side effect, so every assertion reads the string an operator actually sees.
//
// 🔴 WHY THIS FILE EXISTS. A declared BOOLEAN property has THREE states here — unset,
// true, false — and the shared command form has two, because for a command a checkbox
// is right: an actuation must be complete. Reusing that serializer for a stored asset
// fact wrote `false` into every optional boolean the operator never touched, so
// editing an unrelated property recorded a claim nobody made. The API tells absent
// from false; nothing else in the estate would notice the console stopped doing so.
//
// The other half is the counterweight: omitting an unset boolean is only safe while a
// boolean the operator DID set still round-trips, and while a required one left unset
// is refused by the form rather than by the server.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['device:write'] } }) }));

import { AssetPropertiesPanel } from './AssetPropertiesPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
});

// One optional STRING and one optional BOOLEAN: the pair that produced the defect,
// since editing the string is what wrote the boolean.
const CONTRACT = JSON.stringify([
  { name: 'vendor', dataType: 'STRING' },
  { name: 'serviced', dataType: 'BOOLEAN' },
]);

function assetWith(properties: string | null) {
  return {
    id: '1',
    token: 'p-1',
    name: 'Pump 1',
    description: null,
    metadata: null,
    properties,
    createdAt: null,
    assetType: { id: '2', token: 'pump', name: 'Pump', icon: null,
      backgroundColor: null, foregroundColor: null, borderColor: null },
  } as never;
}

/** The `properties` string the panel sent on its updateAsset call. */
function sentProperties(): string | null | undefined {
  const call = gqlMock.mock.calls.find(
    (c) => JSON.stringify(c[2] ?? {}).includes('"token":"p-1"') && (c[2] as Record<string, unknown>).request,
  );
  const request = (call?.[2] as { request?: Record<string, unknown> } | undefined)?.request;
  return request?.properties as string | null | undefined;
}

async function renderPanel(properties: string | null, contract = CONTRACT) {
  // First call: the active contract. Every later call: the update.
  gqlMock.mockImplementation((_area: string, _doc: unknown, vars: Record<string, unknown>) => {
    if (vars && 'request' in vars) return Promise.resolve({ updateAsset: assetWith(null) });
    return Promise.resolve({ activeAssetTypeVersion: { version: 1, label: null, publishedAt: '', propertySchema: contract } });
  });
  render(<AssetPropertiesPanel asset={assetWith(properties)} onSaved={() => {}} />);
  await screen.findByText('vendor');
}

describe('AssetPropertiesPanel booleans', () => {
  it('omits a boolean the operator never set, even when another property is edited', async () => {
    await renderPanel(null);

    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'Acme' } });
    fireEvent.click(screen.getByRole('button', { name: /save|guardar/i }));

    await waitFor(() => expect(sentProperties()).toBeTruthy());
    const doc = JSON.parse(sentProperties() as string);
    expect(doc).toEqual({ vendor: 'Acme' });
    expect('serviced' in doc).toBe(false);
  });

  it('round-trips a boolean the operator did set', async () => {
    await renderPanel(null);

    fireEvent.click(screen.getByRole('radio', { name: /true|verdadero/i }));
    fireEvent.click(screen.getByRole('button', { name: /save|guardar/i }));

    await waitFor(() => expect(sentProperties()).toBeTruthy());
    expect(JSON.parse(sentProperties() as string)).toEqual({ serviced: true });
  });

  it('keeps an explicit false, which is not the same as unset', async () => {
    await renderPanel(JSON.stringify({ serviced: false }));

    fireEvent.click(screen.getByRole('button', { name: /save|guardar/i }));

    await waitFor(() => expect(sentProperties()).toBeTruthy());
    expect(JSON.parse(sentProperties() as string)).toEqual({ serviced: false });
  });

  it('refuses to save a REQUIRED boolean left unset, rather than omitting it', async () => {
    await renderPanel(
      null,
      JSON.stringify([
        { name: 'vendor', dataType: 'STRING' },
        { name: 'serviced', dataType: 'BOOLEAN', required: true },
      ]),
    );

    fireEvent.click(screen.getByRole('button', { name: /save|guardar/i }));

    // Nothing was sent: validateParams skips BOOLEAN entirely, so without the panel's
    // own check this would have been a silent omission refused by the server instead.
    await waitFor(() => expect(sentProperties()).toBeUndefined());
  });
});
