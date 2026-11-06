// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An alarm's last value is a raw float off the wire (16.24614270883714). The list prints
// it the way a metric reads on a dashboard card, not at full float width. Only the
// transport and the live feed are faked.
import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'ann', authorities: ['*'], scopeAnchors: [] } }),
}));
vi.mock('@/lib/hooks/use-alarm-stream', () => ({ useAlarmStream: () => ({ status: 'live' }) }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  const alarm = (token: string, lastValue: number | null) => ({
    id: token,
    token,
    originatorType: 'DEVICE',
    originatorId: '1',
    originatorToken: 'therm-1',
    alarmKey: `key-${token}`,
    metricKey: 'temperature',
    state: 'ACTIVE',
    acknowledged: false,
    severity: 'MAJOR',
    raisedTime: '2026-10-01T00:00:00Z',
    clearedTime: null,
    acknowledgedTime: null,
    acknowledgedBy: null,
    lastValue,
  });
  return {
    ...actual,
    gql: () =>
      Promise.resolve({
        alarms: {
          results: [alarm('a', 16.24614270883714), alarm('b', 0.000123456789), alarm('c', 42), alarm('d', null)],
          pagination: { pageStart: 1, pageEnd: 4, totalRecords: 4 },
        },
      }),
  };
});

import AlarmsPage from './AlarmsPage';

afterEach(cleanup);

const valueCell = (key: string) =>
  screen.getByText(key).closest('tr')!.querySelectorAll('td')[4]?.textContent;

describe('AlarmsPage value column', () => {
  it('rounds the value like a metric on a dashboard card', async () => {
    render(<AlarmsPage />);
    await screen.findByText('key-a');
    expect(valueCell('key-a')).toBe('16.246');
    // Under 1 keeps significant digits instead of rounding to nothing.
    expect(valueCell('key-b')).toBe('0.0001235');
    expect(valueCell('key-c')).toBe('42');
    expect(valueCell('key-d')).toBe('—');
    expect(screen.queryByText(/16\.2461427/)).toBeNull();
  });
});
