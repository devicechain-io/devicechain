// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The ingest-failures view: the reason chips narrow the list, a reason this console has no wording
// for is shown with its number (never as a neighbour), the device filter is seeded from the link a
// device page offers, and nothing on the screen promises a retry.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';

const listFailedEvents = vi.fn();
const getFailedEventSummary = vi.fn();
vi.mock('@/lib/api/ingest-failures', () => ({
  listFailedEvents: (...a: unknown[]) => listFailedEvents(...a),
  getFailedEventSummary: (...a: unknown[]) => getFailedEventSummary(...a),
}));

// The caller's claims, swapped per test: the page is for device:write, not the viewer baseline.
let authorities = ['device:write'];
let scopeAnchors: string[] = [];
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    claims: { tenant: 'acme', username: 'ann', roles: [], authorities, typ: 'access', scopeAnchors },
  }),
}));

import IngestFailuresPage from './IngestFailuresPage';

const row = (over: Record<string, unknown>) => ({
  id: 'a1',
  occurredTime: '2026-11-02T12:00:00Z',
  sourceArea: 'device-management',
  reason: 'DEVICE_NOT_FOUND',
  reasonCode: 3,
  deviceToken: 'ghost-1',
  eventType: 2,
  error: 'no such device',
  correlation: '',
  suppressed: 0,
  overflow: false,
  payloadStatus: 'AVAILABLE',
  ...over,
});

beforeEach(async () => {
  authorities = ['device:write'];
  scopeAnchors = [];
  listFailedEvents.mockReset().mockResolvedValue({
    results: [
      row({ id: 'a3', suppressed: 41, payloadStatus: 'EXPIRED' }),
      row({ id: 'a2', reason: 'UNKNOWN', reasonCode: 99, deviceToken: null, error: 'x', payloadStatus: 'NOT_RETAINED' }),
      row({ id: 'a1', overflow: true, deviceToken: null }),
    ],
    pagination: { pageStart: 1, pageEnd: 3, totalRecords: 3 },
  });
  getFailedEventSummary.mockReset().mockResolvedValue([
    { reason: 'DEVICE_NOT_FOUND', reasonCode: 3, records: 43, suppressed: 41, devices: [{ deviceToken: 'ghost-1', records: 42 }] },
  ]);
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

const renderAt = (url: string) =>
  render(
    <MemoryRouter initialEntries={[url]}>
      <IngestFailuresPage />
    </MemoryRouter>,
  );

describe('IngestFailuresPage', () => {
  it('shows the rows, the folded-in count and an unknown reason with its number', async () => {
    renderAt('/ingest-failures');
    expect(await screen.findAllByText('no such device')).toBeTruthy();
    expect(screen.getByText('+41 more')).toBeTruthy();
    expect(screen.getByText('Reason code 99')).toBeTruthy();
    expect(screen.getByText('Further records for this reason')).toBeTruthy();
    // an expired original reads as expired, on the row that has one
    expect(screen.getAllByText(/no longer held/)).toHaveLength(1);
    // a record that never held one says so, in its own words
    expect(screen.getAllByText('The original was not kept.')).toHaveLength(1);
    // the chip carries the summary's count
    expect(await screen.findByText(/Unknown device · 43/)).toBeTruthy();
  });

  it('narrows by reason when a chip is pressed, and says what the reason means', async () => {
    renderAt('/ingest-failures');
    await screen.findAllByText('no such device');
    fireEvent.click(await screen.findByText(/Unknown device · 43/));
    await waitFor(() =>
      expect(listFailedEvents).toHaveBeenLastCalledWith(expect.objectContaining({ reason: 'DEVICE_NOT_FOUND' })),
    );
    expect(screen.getByText(/No device has the token the sender named/)).toBeTruthy();
    expect(screen.getByText(/Most affected: ghost-1 \(42\)/)).toBeTruthy();
  });

  it('seeds the device filter from the link a device page offers', async () => {
    renderAt('/ingest-failures?device=ghost-1');
    await waitFor(() =>
      expect(listFailedEvents).toHaveBeenCalledWith(expect.objectContaining({ deviceToken: 'ghost-1' })),
    );
  });

  it('offers decoder failures as a reason and seeds the filter from the link the Decoder tab offers', async () => {
    renderAt('/ingest-failures?reason=DECODE_FAILED');
    await waitFor(() =>
      expect(listFailedEvents).toHaveBeenCalledWith(expect.objectContaining({ reason: 'DECODE_FAILED' })),
    );
    expect(screen.getByRole('button', { name: /Decoder failed/ }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByText(/payload decoder could not turn an uplink into readings/)).toBeTruthy();
  });

  it('ignores a reason in the link that it does not know', async () => {
    renderAt('/ingest-failures?reason=NOPE');
    await waitFor(() => expect(listFailedEvents).toHaveBeenCalled());
    expect(listFailedEvents).not.toHaveBeenCalledWith(expect.objectContaining({ reason: 'NOPE' }));
  });

  it('never promises to retry or recover anything', async () => {
    renderAt('/ingest-failures');
    await screen.findAllByText('no such device');
    expect(document.body.textContent ?? '').not.toMatch(/retry|recover/i);
  });

  it('is refused to a viewer holding only the read-only baseline, and fires no query', async () => {
    authorities = ['event:read', 'device:read', 'alarm:read'];
    renderAt('/ingest-failures');
    expect(await screen.findByText(/members who manage devices/)).toBeTruthy();
    expect(listFailedEvents).not.toHaveBeenCalled();
    expect(getFailedEventSummary).not.toHaveBeenCalled();
  });

  it('is refused to a member limited to some customers, even holding device:write', async () => {
    scopeAnchors = ['customer:acme'];
    renderAt('/ingest-failures');
    expect(await screen.findByText(/members who manage devices/)).toBeTruthy();
    expect(listFailedEvents).not.toHaveBeenCalled();
  });
});
