// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI providers list: loading, empty and error states, and a populated row that opens
// the provider. A failed read must show the failure, never an empty table.

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, pageOf, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock('@/lib/api/ai-inference-admin', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/ai-inference-admin')>()),
  listAiProviders: (...a: unknown[]) => h.list(...a),
  listAiProviderKinds: () => Promise.resolve([]),
}));

import AiProvidersPage from './AiProvidersPage';

const t = (key: string) => i18n.t(key);

function row() {
  return { token: 'openai-main', name: 'Main provider', kind: 'OPENAI', model: 'gpt-x', hasSecret: true, enabled: true };
}

describe('AiProvidersPage', () => {
  beforeEach(() => {
    h.list.mockReset();
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<AiProvidersPage />);
    expect(screen.getByRole('status').textContent).toBe(t('aiProviders:loadingProviders'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when none is registered', async () => {
    h.list.mockResolvedValue(pageOf([]));
    renderListPage(<AiProvidersPage />);
    expect(await screen.findByText(t('aiProviders:emptyProviders'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<AiProvidersPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('aiProviders:emptyProviders'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists a row that opens its detail route', async () => {
    h.list.mockResolvedValue(pageOf([row()]));
    renderListPage(<AiProvidersPage />);
    const rowEl = await screen.findByRole('button', { name: /Main provider/ });
    expect(screen.getByRole('table')).toBeTruthy();
    fireEvent.click(rowEl);
    expect(screen.getByTestId('where').textContent).toBe('/admin/ai-providers/openai-main');
  });
});
