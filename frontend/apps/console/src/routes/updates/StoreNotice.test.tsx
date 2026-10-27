// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The store banner speaks only when the service answered NO. Faked: the GraphQL transport.

import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { StoreNotice, StoreNoticeView } from './StoreNotice';

const info = (objectStoreConfigured: boolean) => ({
  updateManagementInfo: {
    area: 'update-management',
    allowUnverifiedArtifacts: false,
    maxArtifactBytes: '1',
    downloadsConfigured: true,
    objectStoreConfigured,
  },
});

describe('StoreNotice', () => {
  afterEach(() => {
    cleanup();
    gqlMock.mockReset();
  });

  it('warns when the service has no object store', async () => {
    gqlMock.mockResolvedValue(info(false));
    render(<StoreNotice />);
    expect(await screen.findByTestId('store-notice')).toBeTruthy();
  });

  it('says nothing when a store is configured', async () => {
    gqlMock.mockResolvedValue(info(true));
    render(<StoreNotice />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());
    expect(screen.queryByTestId('store-notice')).toBeNull();
  });

  it('says nothing when the read failed: an unread answer is not "no store"', async () => {
    gqlMock.mockRejectedValue(new Error('boom'));
    render(<StoreNotice />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());
    expect(screen.queryByTestId('store-notice')).toBeNull();
  });

  it('the view speaks only for an explicit false', () => {
    const { rerender } = render(<StoreNoticeView objectStoreConfigured={false} />);
    expect(screen.queryByTestId('store-notice')).toBeTruthy();
    rerender(<StoreNoticeView objectStoreConfigured={undefined} />);
    expect(screen.queryByTestId('store-notice')).toBeNull();
    rerender(<StoreNoticeView objectStoreConfigured={true} />);
    expect(screen.queryByTestId('store-notice')).toBeNull();
  });
});
