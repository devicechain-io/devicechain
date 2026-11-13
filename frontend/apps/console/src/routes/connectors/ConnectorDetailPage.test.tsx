// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import i18n from 'i18next';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';
import en from '@/i18n/locales/en/connectors.json';
import es from '@/i18n/locales/es/connectors.json';

const { getConnector, updateConnector, toast } = vi.hoisted(() => ({
  getConnector: vi.fn(),
  updateConnector: vi.fn(),
  toast: vi.fn(),
}));
vi.mock('@/lib/api/connectors', () => ({ getConnector, updateConnector }));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: ['connector:write'] } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast }) }));
vi.mock('./ConnectorVersionsPanel', () => ({ ConnectorVersionsPanel: () => null }));

import ConnectorDetailPage from './ConnectorDetailPage';

beforeEach(async () => {
  vi.resetAllMocks();
  await i18n.changeLanguage('en');
  getConnector.mockResolvedValue({
    id: 'connector-id', token: 'pager', name: 'Original', description: 'Original description',
    type: 'mqtt', config: '{"urls":["tcp://broker:1883"],"topic":"alerts"}',
    hasSecret: false, updatedAt: '2026-11-10T12:00:00Z',
  });
});
afterEach(async () => {
  cleanup();
  await i18n.changeLanguage('en');
});

async function editAndSave(saveLabel: string) {
  render(
    <MemoryRouter initialEntries={['/connectors/pager']}>
      <Routes><Route path="/connectors/:token" element={<ConnectorDetailPage />} /></Routes>
    </MemoryRouter>,
  );
  const name = await screen.findByLabelText(i18n.t('common:colName'));
  fireEvent.change(name, { target: { value: 'My unsaved name' } });
  fireEvent.change(screen.getByLabelText(i18n.t('common:colDescription')), {
    target: { value: 'My unsaved description' },
  });
  fireEvent.click(screen.getByRole('button', { name: saveLabel }));
  await waitFor(() => expect(updateConnector).toHaveBeenCalledTimes(1));
}

function expectDraftRetained(saveLabel: string) {
  expect((screen.getByLabelText(i18n.t('common:colName')) as HTMLInputElement).value)
    .toBe('My unsaved name');
  expect((screen.getByLabelText(i18n.t('common:colDescription')) as HTMLTextAreaElement).value)
    .toBe('My unsaved description');
  expect((screen.getByRole('button', { name: saveLabel }) as HTMLButtonElement).disabled).toBe(false);
  expect(getConnector).toHaveBeenCalledTimes(1);
}

it.each([['en', en], ['es', es]] as const)(
  'uses the structured stale code and retains the draft in %s', async (locale, catalog) => {
    await i18n.changeLanguage(locale);
    updateConnector.mockRejectedValue(new GraphQLRequestError('Completely reworded refusal', 200, [
      { message: 'An unrelated error', extensions: { code: 'OTHER' } },
      { message: 'The revision is obsolete', extensions: { code: 'STALE_WRITE' } },
    ]));
    await editAndSave(catalog.saveDraft);
    await screen.findByText(catalog.staleWrite);
    expect(toast).toHaveBeenCalledWith(catalog.staleWrite, 'error');
    expect(screen.queryByText('Completely reworded refusal')).toBeNull();
    expectDraftRetained(catalog.saveDraft);
    expect(updateConnector).toHaveBeenCalledWith('pager', expect.objectContaining({
      expectedUpdatedAt: '2026-11-10T12:00:00Z', name: 'My unsaved name',
    }));
  },
);

it.each([
  new Error('Network unavailable'),
  new GraphQLRequestError('Duplicate token', 200, [{
    message: 'Duplicate token', extensions: { code: 'CONFLICT' },
  }]),
  new GraphQLRequestError('modified by another writer', 200),
])('keeps other failures generic: %s', async (error) => {
  updateConnector.mockRejectedValue(error);
  await editAndSave(en.saveDraft);
  await screen.findByText(error.message);
  expect(toast).toHaveBeenCalledWith(error.message, 'error');
  expect(screen.queryByText(en.staleWrite)).toBeNull();
  expectDraftRetained(en.saveDraft);
});
