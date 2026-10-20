// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The property form is rendered from the asset type's contract, and a save writes the values
// against THAT contract. When the asset's type changes (or the contract is being re-read) the
// previous type's contract is only the answer to a different question: offering its fields and
// Save would let the operator fill in properties the new type does not declare. The panel
// therefore reads the strict `current` answer, not the sticky `data`.
import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['device:write'] } }) }));

import { AssetPropertiesPanel } from './AssetPropertiesPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
});

function assetOfType(typeToken: string) {
  return {
    id: '1',
    token: 'p-1',
    name: 'Pump 1',
    description: null,
    metadata: null,
    properties: null,
    createdAt: null,
    assetType: { id: typeToken, token: typeToken, name: typeToken, icon: null,
      backgroundColor: null, foregroundColor: null, borderColor: null },
  } as never;
}

const contract = (field: string) => ({
  activeAssetTypeVersion: {
    version: 1,
    label: null,
    publishedAt: '',
    propertySchema: JSON.stringify([{ name: field, dataType: 'STRING' }]),
  },
});

// 'pump' always answers; any other type is answered by `other`.
function serve(other: () => Promise<unknown>) {
  gqlMock.mockImplementation((...args: unknown[]) => {
    const vars = args[2] as { token?: string } | undefined;
    return vars?.token === 'pump' ? Promise.resolve(contract('vendor')) : other();
  });
}

describe('AssetPropertiesPanel when the asset type changes', () => {
  it('does not offer the previous type’s fields while the new contract is loading', async () => {
    serve(() => new Promise(() => {}));
    const { rerender } = render(<AssetPropertiesPanel asset={assetOfType('pump')} onSaved={() => {}} />);
    await screen.findByText('vendor');
    expect(screen.getByRole('button', { name: 'Save' })).toBeTruthy();

    rerender(<AssetPropertiesPanel asset={assetOfType('valve')} onSaved={() => {}} />);

    await waitFor(() => expect(screen.queryByText('vendor')).toBeNull());
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
  });
});
