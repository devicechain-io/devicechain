// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import { AreaCapabilityProvider } from '@/lib/capabilities';
import { FUNCTIONAL_AREAS, FunctionalAreasPanel } from './FunctionalAreasPanel';
import SettingsPage from './SettingsPage';
import en from '@/i18n/locales/en/adminSettings.json';
import es from '@/i18n/locales/es/adminSettings.json';
import zh from '@/i18n/locales/zh-CN/adminSettings.json';
import HELPERS_TPL from '../../../../../../../deploy/helm/devicechain/templates/_helpers.tpl?raw';

const { listAreas, listSettings, setSetting, clearSetting } = vi.hoisted(() => ({
  listAreas: vi.fn(), listSettings: vi.fn(), setSetting: vi.fn(), clearSetting: vi.fn(),
}));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isIdentityAuthenticated: true, isAuthenticated: false, isLoading: false }),
}));
vi.mock('@/lib/api/admin', () => ({ listAdminFunctionalAreas: () => listAreas() }));
vi.mock('@/lib/api/settings', () => ({
  listSettings: () => listSettings(), setSetting, clearSetting,
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn() }));

function mount(child = <FunctionalAreasPanel />) {
  return render(<MemoryRouter><AreaCapabilityProvider>{child}</AreaCapabilityProvider></MemoryRouter>);
}

function row(area: string) {
  return within(screen.getByText(area, { selector: 'code' }).closest('tr')!);
}

beforeEach(() => {
  vi.resetAllMocks();
  listAreas.mockResolvedValue(['user-management', 'mcp']);
  listSettings.mockResolvedValue([]);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe('functional areas', () => {
  it('lists the complete deployment catalog once, including areas without browser endpoints', () => {
    const line = HELPERS_TPL.split('\n').find((line) => line.includes('$known := list'))!;
    const known = [...line.matchAll(/"([^"]+)"/g)].map((match) => match[1]);
    expect(known.length).toBeGreaterThan(5);
    expect([...FUNCTIONAL_AREAS].sort()).toEqual(known.sort());
    expect(new Set(FUNCTIONAL_AREAS).size).toBe(FUNCTIONAL_AREAS.length);
  });

  it('shows enabled and undeployed areas, their purpose and the exact enable command', async () => {
    mount();
    await waitFor(() => expect(row('mcp').getByText('Enabled')).toBeTruthy());
    expect(row('user-management').getByText('Enabled')).toBeTruthy();
    expect(row('outbound-connectors').getByText('Not enabled')).toBeTruthy();
    expect(row('outbound-connectors').getByText('Connects actions to external services.')).toBeTruthy();
    expect(row('outbound-connectors').getByText('dcctl enable-area <provider> <instance> outbound-connectors')).toBeTruthy();
    expect(row('mcp').queryByText(/dcctl enable-area/)).toBeNull();
    expect(screen.getAllByRole('row')).toHaveLength(FUNCTIONAL_AREAS.length + 1);
    expect(screen.queryByRole('button')).toBeNull();
    expect(setSetting).not.toHaveBeenCalled();
    expect(clearSetting).not.toHaveBeenCalled();
  });

  it('does not label pending discovery as enabled or not enabled', () => {
    listAreas.mockReturnValue(new Promise(() => {}));
    mount();
    expect(screen.getAllByText('Unknown')).toHaveLength(FUNCTIONAL_AREAS.length);
    expect(screen.queryByText('Not enabled')).toBeNull();
    expect(screen.queryByText('Enabled')).toBeNull();
    expect(screen.queryByText(/dcctl enable-area/)).toBeNull();
  });

  it.each(['failure', 'malformed'])('keeps status unknown after discovery %s', async (kind) => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    if (kind === 'failure') listAreas.mockRejectedValue(new Error('offline'));
    else listAreas.mockResolvedValue([]);
    mount();
    await waitFor(() => expect(warn).toHaveBeenCalled());
    expect(screen.getAllByText('Unknown')).toHaveLength(FUNCTIONAL_AREAS.length);
    expect(screen.queryByText('Not enabled')).toBeNull();
    expect(row('mcp').getByText(/Deployment status is unavailable/)).toBeTruthy();
  });

  it('is reachable as a settings tab without editing settings', async () => {
    listSettings.mockResolvedValue([{ key: 'future.setting', value: '{}', description: '', overridden: false }]);
    mount(<SettingsPage />);
    const tab = await screen.findByRole('tab', { name: 'Functional areas' });
    fireEvent.mouseDown(tab);
    await waitFor(() => expect(row('mcp').getByText('Enabled')).toBeTruthy());
    expect(screen.queryByRole('button', { name: /save override/i })).toBeNull();
    fireEvent.mouseDown(screen.getByRole('tab', { name: 'future.setting' }));
    expect(screen.getByRole('textbox', { name: /raw json/i })).toBeTruthy();
    expect(setSetting).not.toHaveBeenCalled();
  });

  it.each(['empty', 'failed'])('stays reachable when settings are %s', async (state) => {
    if (state === 'failed') listSettings.mockRejectedValue(new Error('settings unavailable'));
    mount(<SettingsPage />);
    expect(await screen.findByRole('tab', { name: 'Functional areas' })).toBeTruthy();
    await waitFor(() => expect(row('mcp').getByText('Enabled')).toBeTruthy());
  });

  it.each([['en', en], ['es', es], ['zh-CN', zh]] as const)('renders the complete %s catalog', async (lng, catalog) => {
    const i18n = createInstance();
    await i18n.init({ lng, resources: { [lng]: { adminSettings: catalog } }, interpolation: { escapeValue: false } });
    mount(<I18nextProvider i18n={i18n}><FunctionalAreasPanel /></I18nextProvider>);
    expect(screen.getByRole('region', { name: catalog.functionalAreasTitle })).toBeTruthy();
    await waitFor(() => expect(row('mcp').getAllByText(catalog.functionalAreasAvailable).length).toBeGreaterThan(0));
    for (const area of FUNCTIONAL_AREAS) {
      expect(row(area).getByText(catalog[`functionalAreaName_${area}`])).toBeTruthy();
      expect(row(area).getByText(catalog[`functionalAreaDescription_${area}`])).toBeTruthy();
    }
    expect(screen.queryByText(/functionalAreas\./)).toBeNull();
  });
});
