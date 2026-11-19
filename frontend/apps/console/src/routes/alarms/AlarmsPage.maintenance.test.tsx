// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/config';
import { useLayoutEffect } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ gql: vi.fn(), download: vi.fn(), search: '' }));
vi.mock('@devicechain/client', async original => ({ ...await original<typeof import('@devicechain/client')>(), gql: h.gql }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['*'], scopeAnchors: [] } }) }));
vi.mock('@/lib/hooks/use-alarm-stream', () => ({ useAlarmStream: () => ({ status: 'live' }) }));
vi.mock('@/lib/csv', async original => ({ ...await original<typeof import('@/lib/csv')>(), downloadCsv: h.download }));
import { GraphQLRequestError } from '@devicechain/client';
import { ToastProvider } from '@/components/ui/toast';
import AlarmsPage from './AlarmsPage';
import { DeviceAlarmsPanel } from '@/routes/devices/DeviceAlarmsPanel';

const alarm = { id: 'a', token: 'a', originatorType: 'device', originatorToken: 'pump', alarmKey: 'overheat', metricKey: 'temperature', state: 'CLEARED', severity: 'MAJOR', acknowledged: false, raisedTime: '2026-01-01T00:00:00Z' };
const page = (results = [alarm], totalRecords = results.length) => ({ results, pagination: { pageStart: 1, pageEnd: results.length, totalRecords } });
const refusal = () => {
  const message = 'Field "raisedDuringSuppression" is not defined by type "AlarmSearchCriteria".';
  return Promise.reject(new GraphQLRequestError(message, 200, [{ message }]));
};
let filteredUnavailable = false;
let exportLosesCapability = false;
let stamp: string | null = null;
let unfilteredAddresses: string[] = [];
let exportPages: number[] = [];
beforeEach(() => {
  filteredUnavailable = false; exportLosesCapability = false; stamp = null;
  unfilteredAddresses = []; exportPages = []; h.search = ''; h.download.mockReset();
  h.gql.mockReset().mockImplementation((_service, doc, variables) => {
    const op = /query\s+(\w+)/.exec(String(doc))?.[1];
    switch (op) {
      case 'AlarmMaintenanceWindows': return Promise.resolve({ alarmSuppressionWindows: page([]) });
      case 'AlarmMaintenanceStatus': return Promise.resolve({ alarmSuppressionStatus: variables.tokens.map((alarmToken: string) => ({ alarmToken, suppression: null })) });
      case 'AlarmMaintenanceHistoryMarks': return Promise.resolve({ alarmsByToken: variables.tokens.map((token: string) => ({ token, raisedDuringSuppression: stamp })) });
      case 'AlarmMaintenanceHistory':
        if (filteredUnavailable) return refusal();
        if (exportLosesCapability && variables.pageSize !== 25) {
          exportPages.push(variables.pageNumber);
          if (variables.pageNumber === 2) return refusal();
          return Promise.resolve({ alarms: page(Array.from({ length: variables.pageSize }, () => alarm), variables.pageSize + 1) });
        }
        return Promise.resolve({ alarms: page() });
      case 'Alarms':
        unfilteredAddresses.push(h.search);
        return Promise.resolve({ alarms: page() });
      default: return Promise.resolve({ deviceMetricVocabulary: null });
    }
  });
});
afterEach(cleanup);
function Address() {
  const { search } = useLocation();
  useLayoutEffect(() => { h.search = search; }, [search]);
  return <output data-testid="address">{search}</output>;
}
const show = (search = '', device = false) => render(<MemoryRouter initialEntries={[`/alarms${search}`]}><ToastProvider>
  {device ? <DeviceAlarmsPanel deviceToken="pump" /> : <AlarmsPage />}<Address />
</ToastProvider></MemoryRouter>);

describe('maintenance history capability loss', () => {
  it('removes the history filter from the address before loading unfiltered rows', async () => {
    filteredUnavailable = true;
    show('?state=CLEARED&maintenance=true');
    await screen.findByText('overheat');
    expect(screen.getByTestId('address').textContent).toBe('?state=CLEARED');
    expect(unfilteredAddresses).toHaveLength(1);
    expect(unfilteredAddresses[0]).toBe('?state=CLEARED');
    expect(screen.queryByRole('combobox', { name: 'Raised during maintenance' })?.textContent ?? '').not.toContain('Raised during maintenance');
  });
  it('fails a filtered export if a later page loses capability without downloading or fetching unfiltered pages', async () => {
    exportLosesCapability = true;
    show('?maintenance=true');
    await screen.findByText('overheat');
    fireEvent.click(screen.getByRole('button', { name: 'Export CSV' }));
    await waitFor(() => expect(screen.getAllByText(/Export failed:.*Maintenance history is unavailable/).length).toBeGreaterThan(0));
    expect(exportPages).toEqual([1, 2]);
    expect(unfilteredAddresses).toEqual([]);
    expect(h.download).not.toHaveBeenCalled();
    expect(screen.getByTestId('address').textContent).toBe('?maintenance=true');
  });
});

describe('maintenance cycle history markers', () => {
  it.each(['list', 'device'])('shows a stamped cleared alarm without live suppression in the %s view', async view => {
    stamp = 'ended-window';
    show('', view === 'device');
    expect(await screen.findByText('Raised during maintenance')).toBeTruthy();
    expect(screen.getByText('overheat').closest('tr')!.textContent).toContain('Cleared');
    expect(screen.queryByText(/Maintenance.*until/)).toBeNull();
  });
  it.each(['list', 'device'])('renders a restricted history stamp with no token, window name, or maintenance link in the %s view', async view => {
    stamp = 'restricted';
    show('', view === 'device');
    expect(await screen.findByText('Raised during maintenance')).toBeTruthy();
    expect(document.body.textContent).not.toContain('restricted');
    expect(document.querySelector('a[href^="/alarm-maintenance/"]')).toBeNull();
  });
});
