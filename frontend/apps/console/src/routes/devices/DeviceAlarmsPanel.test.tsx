// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Alarms tab on a device: it asks for this device's alarms only, prints values with the
// unit the profile declares, acknowledges one alarm, and says so when there are none.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ gql: vi.fn(), authorities: ['*'] as string[] }));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'ann', authorities: h.authorities, scopeAnchors: [] } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import { ToastProvider } from '@/components/ui/toast';
import { DeviceAlarmsPanel } from './DeviceAlarmsPanel';

const alarm = (token: string, o: Partial<Record<string, unknown>> = {}) => ({
  id: token,
  token,
  originatorType: 'device',
  originatorId: '1',
  originatorToken: 'therm-1',
  alarmKey: `key-${token}`,
  metricKey: 'temperature',
  state: 'ACTIVE',
  acknowledged: false,
  severity: 'CRITICAL',
  raisedTime: '2026-10-01T00:00:00Z',
  clearedTime: null,
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 91.25,
  ...o,
});

let rows: ReturnType<typeof alarm>[] = [];
let sent: { op: string; variables: any }[] = [];

beforeEach(() => {
  sent = [];
  h.authorities = ['*'];
  rows = [alarm('a'), alarm('b', { acknowledged: true })];
  h.gql.mockImplementation((_service: string, doc: unknown, variables: any) => {
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'Alarms':
        return Promise.resolve({
          alarms: { results: rows, pagination: { pageStart: 1, pageEnd: rows.length, totalRecords: rows.length } },
        });
      case 'DeviceMetricVocabulary':
        return Promise.resolve({
          deviceMetricVocabulary: { metrics: [{ metricKey: 'temperature', unit: '°C', dataType: 'DOUBLE' }] },
        });
      case 'AcknowledgeAlarm':
        return Promise.resolve({ acknowledgeAlarm: { id: variables.token } });
      default:
        return Promise.resolve({});
    }
  });
});
afterEach(cleanup);

const renderPanel = () =>
  render(
    <MemoryRouter>
      <ToastProvider>
        <DeviceAlarmsPanel deviceToken="therm-1" />
      </ToastProvider>
    </MemoryRouter>,
  );

describe('DeviceAlarmsPanel', () => {
  it('asks for the alarms raised on this device only', async () => {
    renderPanel();
    await screen.findByText('key-a');
    expect(sent.find((s) => s.op === 'Alarms')!.variables.criteria).toMatchObject({
      originatorType: 'device',
      originatorToken: 'therm-1',
    });
  });

  it('prints each value with its unit', async () => {
    renderPanel();
    await waitFor(() => expect(screen.getByText('key-a').closest('tr')!.textContent).toContain('91.25 °C'));
  });

  it('acknowledges one alarm, and only an active unacknowledged one offers it', async () => {
    renderPanel();
    await screen.findByText('key-a');
    expect(screen.getAllByRole('button', { name: 'Acknowledge' })).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: 'Acknowledge' }));
    await waitFor(() => expect(sent.some((s) => s.op === 'AcknowledgeAlarm')).toBe(true));
    expect(sent.find((s) => s.op === 'AcknowledgeAlarm')!.variables).toEqual({ token: 'a' });
  });

  it('offers no acknowledge without alarm:write', async () => {
    h.authorities = ['alarm:read'];
    renderPanel();
    await screen.findByText('key-a');
    expect(screen.queryByRole('button', { name: 'Acknowledge' })).toBeNull();
  });

  it('says so when the device has no alarms, and links to the tenant list', async () => {
    rows = [];
    renderPanel();
    expect(await screen.findByText('No alarms have been raised on this device.')).toBeTruthy();
    expect((screen.getByRole('link', { name: 'All alarms' }) as HTMLAnchorElement).getAttribute('href')).toBe('/alarms');
  });
});
