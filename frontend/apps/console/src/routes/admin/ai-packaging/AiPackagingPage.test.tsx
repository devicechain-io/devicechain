// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI packaging screen reads three lists (tiers, providers, grants) and joins them.
// It must show loading until the first of them lands, show the failure of ANY of them
// rather than a half-joined screen, and say which prerequisite is missing when the join
// has nothing to show.

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, pageOf, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({ tiers: vi.fn(), providers: vi.fn(), grants: vi.fn() }));

vi.mock('@/lib/api/admin', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/admin')>()),
  listTenantTierCatalog: (...a: unknown[]) => h.tiers(...a),
}));
vi.mock('@/lib/api/ai-inference-admin', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/ai-inference-admin')>()),
  listAiProviders: (...a: unknown[]) => h.providers(...a),
  listAiProviderTierGrants: (...a: unknown[]) => h.grants(...a),
}));

import AiPackagingPage from './AiPackagingPage';

const t = (key: string) => i18n.t(key);

const provider = { token: 'openai-main', name: 'Main provider', kind: 'OPENAI', model: 'gpt-x', hasSecret: true, enabled: true };
const tier = { token: 'gold', name: 'Gold', tenantCount: 3 };

describe('AiPackagingPage', () => {
  beforeEach(() => {
    h.tiers.mockReset();
    h.tiers.mockResolvedValue([tier]);
    h.providers.mockReset();
    h.providers.mockResolvedValue(pageOf([provider]));
    h.grants.mockReset();
    h.grants.mockResolvedValue([]);
  });
  afterEach(cleanup);

  it('shows the loading state while the lists are read', () => {
    h.grants.mockReturnValue(never());
    renderListPage(<AiPackagingPage />);
    expect(screen.getByRole('status').textContent).toBe(t('aiPackaging:loadingPackaging'));
  });

  it('says no provider is registered rather than showing empty panels', async () => {
    h.providers.mockResolvedValue(pageOf([]));
    renderListPage(<AiPackagingPage />);
    expect(await screen.findByText(t('aiPackaging:noProvidersRegistered'))).toBeTruthy();
  });

  it('says no tier is defined when the catalog is empty', async () => {
    h.tiers.mockResolvedValue([]);
    renderListPage(<AiPackagingPage />);
    expect(await screen.findByText(t('aiPackaging:noTiersDefined'))).toBeTruthy();
  });

  it('shows the error when any one of the three reads fails', async () => {
    h.grants.mockImplementation(() => Promise.reject(new GraphQLRequestError('grants unavailable', 500)));
    renderListPage(<AiPackagingPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('grants unavailable');
    expect(screen.queryByText(t('aiPackaging:noProvidersRegistered'))).toBeNull();
    expect(screen.queryByText(t('aiPackaging:noTiersDefined'))).toBeNull();
  });

  it('shows a panel for each tier with a link to the provider it can be granted', async () => {
    renderListPage(<AiPackagingPage />);
    expect(await screen.findByText('gold')).toBeTruthy();
    const link = screen.getByRole('link', { name: 'openai-main' });
    expect(link.getAttribute('href')).toBe('/admin/ai-providers/openai-main');
  });
});
