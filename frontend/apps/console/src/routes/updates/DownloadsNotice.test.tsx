// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The download banner speaks only when the service answered NO. Faked: the GraphQL transport.

import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { DownloadsNotice } from './DownloadsNotice';

const info = (downloadsConfigured: boolean) => ({
  updateManagementInfo: { area: 'update-management', allowUnverifiedArtifacts: false, maxArtifactBytes: '1', downloadsConfigured },
});

describe('DownloadsNotice', () => {
  afterEach(() => {
    cleanup();
    gqlMock.mockReset();
  });

  it('warns when the service has no download base', async () => {
    gqlMock.mockResolvedValue(info(false));
    render(<DownloadsNotice />);
    expect(await screen.findByTestId('downloads-notice')).toBeTruthy();
  });

  it('says nothing when downloads are configured', async () => {
    gqlMock.mockResolvedValue(info(true));
    render(<DownloadsNotice />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());
    expect(screen.queryByTestId('downloads-notice')).toBeNull();
  });

  it('says nothing when the read failed: an unread answer is not "not configured"', async () => {
    gqlMock.mockRejectedValue(new Error('boom'));
    render(<DownloadsNotice />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());
    expect(screen.queryByTestId('downloads-notice')).toBeNull();
  });
});
