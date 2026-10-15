// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The paste door waits for the board's lazily loaded widget code before it shows the board, as
// the published door does (load.ts withWidgetCode): without that wait the first frame is a board
// of empty boxes filling in one family at a time. Driven through the real App; only the
// transport, the board renderer and the preload itself are faked, the last so the test can hold
// it open and see that the board is NOT shown while it is in flight.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  gql: vi.fn(),
  preload: vi.fn(),
}));

vi.mock('@devicechain/client', async (orig) => ({
  ...(await orig<typeof import('@devicechain/client')>()),
  gql: (...a: unknown[]) => h.gql(...a),
  setAuthTokenGetter: () => {},
}));
vi.mock('@devicechain/widgets', async (orig) => {
  const React = await import('react');
  return {
    ...(await orig<Record<string, unknown>>()),
    DashboardRenderer: () => React.createElement('div', { 'data-testid': 'board' }),
    useDashboardHub: () => ({}),
    useSlotCandidates: () => undefined,
    preloadWidgets: (...a: unknown[]) => h.preload(...a),
  };
});
vi.mock('@devicechain/dashboards', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  createDeviceResolver: () => ({}),
  createEntityLister: () => ({}),
}));

import App from './App';
import i18n from './i18n/config';
import { LOGIN, SELECT_TENANT } from './queries';
import { resetSsoStateForTests } from './sso';

const t = (key: string) => i18n.t(key);
const FIXTURE = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata', 'sim-dashboards', 'sp-dashboard.json');

function jwt(expSecondsFromNow: number): string {
  return `h.${btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + expSecondsFromNow }))}.s`;
}

beforeEach(async () => {
  window.history.replaceState(null, '', '/dash/');
  h.gql.mockReset();
  h.preload.mockReset();
  h.gql.mockImplementation(async (_area: string, doc: unknown) => {
    if (doc === LOGIN) {
      return { login: { identityToken: 'id', memberships: [{ tenant: 'acme', roles: [], ssoRequired: false }] } };
    }
    if (doc === SELECT_TENANT) return { selectTenant: { accessToken: jwt(3600), refreshToken: jwt(86400) } };
    throw new Error('unexpected document');
  });
  resetSsoStateForTests();
  window.sessionStorage.clear();
  window.localStorage.clear();
  await i18n.changeLanguage('en');
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('the paste door', () => {
  it('shows the board only once the code of its lazily loaded widgets has arrived', async () => {
    let release!: () => void;
    h.preload.mockImplementation(() => new Promise<void>((r) => (release = r)));
    const text = readFileSync(FIXTURE, 'utf8');
    const types = [...new Set((JSON.parse(text) as { widgets: { type: string }[] }).widgets.map((w) => w.type))].sort();

    render(<App />);
    fireEvent.change(screen.getByLabelText(t('signIn:emailLabel')), { target: { value: 'a@b.co' } });
    fireEvent.change(screen.getByLabelText(t('signIn:passwordLabel')), { target: { value: 'pw' } });
    fireEvent.click(screen.getByRole('button', { name: t('signIn:submit') }));
    const definition = await screen.findByPlaceholderText(t('load:definitionPlaceholder'));
    fireEvent.change(definition, { target: { value: text } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: t('load:render') }));
    });

    // Asked for the board's own widget types, and the board waits while the code is in flight.
    expect(h.preload).toHaveBeenCalledTimes(1);
    expect([...new Set(h.preload.mock.calls[0][1] as string[])].sort()).toEqual(types);
    expect(types.length).toBeGreaterThan(1);
    expect(screen.queryByTestId('board')).toBeNull();

    await act(async () => release());
    expect(await screen.findByTestId('board')).toBeTruthy();
  });
});
