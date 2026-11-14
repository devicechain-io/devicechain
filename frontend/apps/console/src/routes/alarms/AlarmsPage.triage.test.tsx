// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Triage on the tenant alarm list: the originator links to its device, the value carries the unit
// the device's profile declares, several alarms are acknowledged in one request, and the filters
// can be seeded from the address. Only the transport and the live feed are faked.
import '@/i18n/config';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, useLocation, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  gql: vi.fn(),
  authorities: ['*'] as string[],
  onEvent: (() => {}) as (ev: unknown) => void,
  onReconnect: (() => {}) as () => void,
}));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'ann', authorities: h.authorities, scopeAnchors: [] } }),
}));
vi.mock('@/lib/hooks/use-alarm-stream', () => ({
  useAlarmStream: (onEvent: (ev: unknown) => void, onReconnect: () => void) => {
    h.onEvent = onEvent;
    h.onReconnect = onReconnect;
    return { status: 'live' };
  },
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import { ToastProvider } from '@/components/ui/toast';
import AlarmsPage from './AlarmsPage';

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
  severity: 'MAJOR',
  raisedTime: '2026-10-01T00:00:00Z',
  clearedTime: null,
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 21.5,
  ...o,
});

let rows: ReturnType<typeof alarm>[] = [];
let sent: { op: string; variables: any }[] = [];

beforeEach(() => {
  sent = [];
  h.authorities = ['*'];
  rows = [
    alarm('a'),
    alarm('b'),
    alarm('c', { acknowledged: true }),
    alarm('d', { state: 'CLEARED' }),
    alarm('e', { originatorType: 'asset', originatorToken: 'dock-9', metricKey: 'load' }),
  ];
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
      case 'AcknowledgeAlarms':
        return Promise.resolve({
          acknowledgeAlarms: { acknowledged: variables.tokens.map((token: string) => ({ token })), refusals: [] },
        });
      default:
        return Promise.resolve({});
    }
  });
});
afterEach(cleanup);

describe('slow alarm reconciles', () => {
  afterEach(() => vi.useRealTimers());

  it('lets slow reads land during a storm and coalesces stream, reconnect and periodic refreshes', async () => {
    const pending: ((value: unknown) => void)[] = [];
    h.gql.mockImplementation((_service: string, doc: unknown) =>
      /query Alarms\b/.test(String(doc))
        ? new Promise((resolve) => pending.push(resolve))
        : Promise.resolve({ deviceMetricVocabulary: null }),
    );
    vi.useFakeTimers();
    const view = renderPage();
    const settle = async (token: string) => {
      await act(async () => pending.shift()!({
        alarms: { results: [alarm(token)], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
      }));
    };
    const storm = async () => {
      for (let i = 0; i < 4; i++) {
        await act(async () => {
          h.onEvent({ token: `event-${i}` });
          h.onReconnect();
          await vi.advanceTimersByTimeAsync(1000);
        });
      }
    };
    await storm();
    await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
    expect(pending).toHaveLength(1);
    await settle('first');
    expect(screen.getByText('key-first')).toBeTruthy();
    expect(pending).toHaveLength(1); // exactly one trailing read
    await storm();
    expect(pending).toHaveLength(1);
    await settle('second');
    expect(screen.getByText('key-second')).toBeTruthy();
    expect(pending).toHaveLength(1);
    view.unmount();
    await settle('after-unmount');
    expect(pending).toHaveLength(0);
  });
});

// Stands in for the rest of the app: shows the address and can move it, as a link or Back would.
function Address() {
  const location = useLocation();
  const navigate = useNavigate();
  return (
    <>
      <output data-testid="address">{location.search}</output>
      <button onClick={() => navigate('/alarms?severity=MINOR')}>go-minor</button>
    </>
  );
}

const renderPage = (search = '') =>
  render(
    <MemoryRouter initialEntries={[`/alarms${search}`]}>
      <ToastProvider>
        <AlarmsPage />
        <Address />
      </ToastProvider>
    </MemoryRouter>,
  );
const rowOf = (key: string) => screen.getByText(key).closest('tr')!;

describe('alarm to device links', () => {
  it('links a device originator to its page, and leaves another kind as text', async () => {
    renderPage();
    await screen.findByText('key-a');
    const link = rowOf('key-a').querySelector('a') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('/devices/therm-1');
    expect(link.textContent).toBe('therm-1');
    expect(rowOf('key-e').querySelector('a')).toBeNull();
    expect(rowOf('key-e').textContent).toContain('dock-9');
  });
});

describe('values with units', () => {
  it('prints the unit the device profile declares, and nothing where it declares none', async () => {
    renderPage();
    await waitFor(() => expect(rowOf('key-a').textContent).toContain('21.5 °C'));
    expect(rowOf('key-e').textContent).not.toContain('°C');
    // One read per device, however many of its alarms are on the page.
    expect(sent.filter((s) => s.op === 'DeviceMetricVocabulary')).toHaveLength(1);
    expect(sent.find((s) => s.op === 'DeviceMetricVocabulary')!.variables).toEqual({ deviceToken: 'therm-1' });
  });

  it('shows the bare value when the vocabulary cannot be read', async () => {
    h.gql.mockImplementation((_s: string, doc: unknown) =>
      /DeviceMetricVocabulary/.test(String(doc))
        ? Promise.reject(new Error('forbidden'))
        : Promise.resolve({ alarms: { results: rows, pagination: { pageStart: 1, pageEnd: 5, totalRecords: 5 } } }),
    );
    renderPage();
    await screen.findByText('key-a');
    expect(rowOf('key-a').textContent).toContain('21.5');
    expect(rowOf('key-a').textContent).not.toContain('°C');
  });
});

describe('bulk acknowledge', () => {
  const box = (key: string) => rowOf(key).querySelector('button[role="checkbox"]') as HTMLButtonElement;

  it('offers a tick only on alarms that can be acknowledged', async () => {
    renderPage();
    await screen.findByText('key-a');
    expect(box('key-a').disabled).toBe(false);
    expect(box('key-c').disabled).toBe(true); // already acknowledged
    expect(box('key-d').disabled).toBe(true); // cleared
  });

  it('acknowledges the ticked alarms in one request, then clears the selection', async () => {
    renderPage();
    await screen.findByText('key-a');
    expect(screen.queryByText(/Acknowledge \d+ selected/)).toBeNull();
    fireEvent.click(box('key-a'));
    fireEvent.click(box('key-b'));
    fireEvent.click(await screen.findByRole('button', { name: 'Acknowledge 2 selected' }));
    await waitFor(() => expect(sent.some((s) => s.op === 'AcknowledgeAlarms')).toBe(true));
    expect(sent.find((s) => s.op === 'AcknowledgeAlarms')!.variables).toEqual({ tokens: ['a', 'b'] });
    expect(await screen.findByText('2 alarms acknowledged')).toBeTruthy();
    await waitFor(() => expect(screen.queryByText(/Acknowledge \d+ selected/)).toBeNull());
  });

  it('forgets the ticks when a filter changes the list under them', async () => {
    renderPage();
    await screen.findByText('key-a');
    fireEvent.click(box('key-a'));
    expect(await screen.findByRole('button', { name: 'Acknowledge 1 selected' })).toBeTruthy();
    fireEvent.click(screen.getByText('All severities'));
    await waitFor(() => expect(document.querySelector('[data-shot-value="MAJOR"]')).not.toBeNull());
    fireEvent.click(document.querySelector('[data-shot-value="MAJOR"]') as HTMLElement);
    await waitFor(() => expect(screen.queryByRole('button', { name: /Acknowledge \d+ selected/ })).toBeNull());
    // The same alarm ticked again is a fresh selection, not an accumulation.
    await waitFor(() => expect(box('key-a').getAttribute('aria-checked')).toBe('false'));
  });

  it('clears the selection from the bar', async () => {
    renderPage();
    await screen.findByText('key-a');
    fireEvent.click(box('key-a'));
    fireEvent.click(await screen.findByRole('button', { name: 'Clear selection' }));
    await waitFor(() => expect(screen.queryByRole('button', { name: /Acknowledge \d+ selected/ })).toBeNull());
  });

  it('drops a tick whose alarm was acknowledged elsewhere while it sat selected', async () => {
    renderPage();
    await screen.findByText('key-a');
    fireEvent.click(box('key-a'));
    fireEvent.click(box('key-b'));
    expect(await screen.findByRole('button', { name: 'Acknowledge 2 selected' })).toBeTruthy();
    // Someone else acknowledges a; the live feed triggers the list to refresh.
    rows = rows.map((r) => (r.token === 'a' ? { ...r, acknowledged: true } : r));
    act(() => h.onEvent({ eventType: 'ACKNOWLEDGED' }));
    expect(await screen.findByRole('button', { name: 'Acknowledge 1 selected' }, { timeout: 4000 })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Acknowledge 1 selected' }));
    await waitFor(() => expect(sent.some((s) => s.op === 'AcknowledgeAlarms')).toBe(true));
    expect(sent.find((s) => s.op === 'AcknowledgeAlarms')!.variables).toEqual({ tokens: ['b'] });
  });

  it('does not bring back a tick for an alarm that left the list and returned', async () => {
    renderPage();
    await screen.findByText('key-a');
    fireEvent.click(box('key-a'));
    expect(await screen.findByRole('button', { name: 'Acknowledge 1 selected' })).toBeTruthy();
    const all = rows;
    rows = all.filter((r) => r.token !== 'a');
    act(() => h.onEvent({ eventType: 'CLEARED' }));
    await waitFor(() => expect(screen.queryByText('key-a')).toBeNull(), { timeout: 4000 });
    rows = all;
    act(() => h.onEvent({ eventType: 'RAISED' }));
    await screen.findByText('key-a', undefined, { timeout: 4000 });
    expect(screen.queryByRole('button', { name: /Acknowledge \d+ selected/ })).toBeNull();
  });

  it('selects every acknowledgeable alarm from the header, and none again', async () => {
    renderPage();
    await screen.findByText('key-a');
    const all = screen.getByRole('checkbox', { name: 'Select all alarms that can be acknowledged' });
    fireEvent.click(all);
    // a, b and e: the acknowledged and the cleared alarms are not selectable.
    expect(await screen.findByRole('button', { name: 'Acknowledge 3 selected' })).toBeTruthy();
    fireEvent.click(all);
    await waitFor(() => expect(screen.queryByText(/Acknowledge \d+ selected/)).toBeNull());
  });

  it('says how many were not found when the server refuses some', async () => {
    h.gql.mockImplementation((_s: string, doc: unknown) =>
      /AcknowledgeAlarms/.test(String(doc))
        ? Promise.resolve({
            acknowledgeAlarms: {
              acknowledged: [{ token: 'a' }],
              refusals: [{ token: 'b', code: 'ALARM_NOT_FOUND', reason: 'gone' }],
            },
          })
        : /DeviceMetricVocabulary/.test(String(doc))
          ? Promise.resolve({ deviceMetricVocabulary: null })
          : Promise.resolve({ alarms: { results: rows, pagination: { pageStart: 1, pageEnd: 5, totalRecords: 5 } } }),
    );
    renderPage();
    await screen.findByText('key-a');
    fireEvent.click(box('key-a'));
    fireEvent.click(box('key-b'));
    fireEvent.click(await screen.findByRole('button', { name: 'Acknowledge 2 selected' }));
    expect(await screen.findByText('1 acknowledged, 1 not found')).toBeTruthy();
  });

  it('offers no ticks to a member without alarm:write', async () => {
    h.authorities = ['alarm:read'];
    renderPage();
    await screen.findByText('key-a');
    expect(screen.queryAllByRole('checkbox')).toHaveLength(0);
  });
});

describe('filters from the address', () => {
  it('follows the address when it changes while the page stays mounted', async () => {
    renderPage('?severity=CRITICAL');
    await screen.findByText('key-a');
    fireEvent.click(screen.getByText('go-minor'));
    await waitFor(() =>
      expect(sent.filter((s) => s.op === 'Alarms').pop()!.variables.criteria).toMatchObject({ severity: 'MINOR' }),
    );
  });

  it('writes a picked filter to the address', async () => {
    renderPage();
    await screen.findByText('key-a');
    fireEvent.click(screen.getByText('All severities'));
    await waitFor(() => expect(document.querySelector('[data-shot-value="MAJOR"]')).not.toBeNull());
    fireEvent.click(document.querySelector('[data-shot-value="MAJOR"]') as HTMLElement);
    await waitFor(() => expect(screen.getByTestId('address').textContent).toBe('?severity=MAJOR'));
  });

  it('keeps acknowledged and state filters in the address too', async () => {
    renderPage('?ack=false');
    await screen.findByText('key-a');
    expect(sent.find((s) => s.op === 'Alarms')!.variables.criteria).toMatchObject({ acknowledged: false });
  });

  it('starts narrowed to the state and severity the address names', async () => {
    renderPage('?state=ACTIVE&severity=CRITICAL');
    await screen.findByText('key-a');
    const first = sent.find((s) => s.op === 'Alarms')!.variables.criteria;
    expect(first).toMatchObject({ state: 'ACTIVE', severity: 'CRITICAL' });
  });

  it('ignores a value that is not one of the choices', async () => {
    renderPage('?state=BOGUS&severity=x');
    await screen.findByText('key-a');
    const first = sent.find((s) => s.op === 'Alarms')!.variables.criteria;
    expect(first.state).toBeNull();
    expect(first.severity).toBeNull();
  });
});
