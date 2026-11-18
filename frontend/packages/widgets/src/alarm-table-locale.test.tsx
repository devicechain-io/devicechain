// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { AlarmRow, WidgetActions, WidgetInstance } from '@devicechain/dashboards';
import { type AlarmStreamState, WidgetSelectProvider } from '@devicechain/widget-sdk';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { AlarmTable } from './widgets/alarm-table';

let originalLang: string | null;
beforeEach(() => {
  originalLang = document.documentElement.getAttribute('lang');
});
afterEach(() => {
  cleanup();
  if (originalLang === null) document.documentElement.removeAttribute('lang');
  else document.documentElement.setAttribute('lang', originalLang);
});

const widget: WidgetInstance = {
  id: 'localized-alarms',
  type: 'alarm-table',
  layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
  options: { groupBy: 'deviceType', selectionTarget: 'selectedDevice' },
};

const alarm = (over: Partial<AlarmRow> = {}): AlarmRow => ({
  token: 'alarm-1',
  originatorType: 'device',
  originatorToken: 'thermostat-01',
  alarmKey: 'over-temperature',
  metricKey: 'temperature',
  state: 'ACTIVE',
  acknowledged: false,
  severity: 'CRITICAL',
  raisedTime: '2026-07-05T12:00:00Z',
  clearedTime: null,
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 87.4,
  deviceType: { token: 'thermostat', name: 'Thermostat' },
  ...over,
});

const state = (over: Partial<AlarmStreamState> = {}): AlarmStreamState => ({
  alarms: [alarm()],
  total: 9,
  loading: false,
  error: null,
  typeTabs: {
    tabs: [{ token: 'thermostat', name: 'Thermostat', active: 1, worstSeverity: 'CRITICAL' }],
    selected: null,
    select: vi.fn(),
    loading: false,
  },
  ...over,
});

const english = {
  headers: ['Severity', 'Status', 'Originator', 'Device type', 'Alarm', 'Value', 'Raised', 'Actions'],
  active: 'Active', acknowledged: 'Acknowledged', cleared: 'Cleared', severity: 'Critical',
  acknowledge: 'Acknowledge alarm over-temperature', clear: 'Clear alarm over-temperature',
  ackText: 'Ack', clearText: 'Clear',
  empty: 'No alarms', loading: 'Loading…', footer: 'Showing 1 of 9',
  drill: 'Show thermostat-01 in the linked widgets', failed: 'Action failed — retry',
};
const spanish = {
  headers: ['Severidad', 'Estado', 'Originador', 'Tipo de dispositivo', 'Alarma', 'Valor', 'Generada', 'Acciones'],
  active: 'Activa', acknowledged: 'Reconocida', cleared: 'Despejada', severity: 'Crítica',
  acknowledge: 'Reconocer alarma over-temperature', clear: 'Despejar alarma over-temperature',
  ackText: 'Reconocer', clearText: 'Despejar',
  empty: 'No hay alarmas', loading: 'Cargando…', footer: 'Mostrando 1 de 9',
  drill: 'Mostrar thermostat-01 en los widgets enlazados', failed: 'La acción falló — inténtalo de nuevo',
};

describe.each([
  { locale: 'en', words: english },
  { locale: 'es', words: spanish },
  { locale: 'es-MX', words: spanish },
])('AlarmTable in $locale', ({ locale, words }) => {
  beforeEach(() => { document.documentElement.lang = locale; });

  it('renders localized columns, severity, status, actions, drill title and shown/total counts', () => {
    const actions: WidgetActions = {
      can: vi.fn(() => true),
      acknowledgeAlarm: vi.fn().mockResolvedValue(undefined),
      clearAlarm: vi.fn().mockResolvedValue(undefined),
    };
    const select = vi.fn();
    render(
      <WidgetSelectProvider select={select}>
        <AlarmTable widget={widget} data={state()} actions={actions} />
      </WidgetSelectProvider>,
    );
    expect(screen.getAllByRole('columnheader').map((el) => el.textContent)).toEqual(words.headers);
    expect(screen.getByRole('cell', { name: words.active })).toBeTruthy();
    expect(screen.getByRole('cell', { name: words.severity })).toBeTruthy();
    expect(screen.getByRole('button', { name: words.acknowledge }).textContent).toBe(words.ackText);
    expect(screen.getByRole('button', { name: words.clear }).textContent).toBe(words.clearText);
    expect(screen.getByText(words.footer)).toBeTruthy();
    // Authored keys, tokens and type names remain data in either language.
    expect(screen.getByRole('cell', { name: 'over-temperature' })).toBeTruthy();
    expect(screen.getByRole('cell', { name: 'Thermostat' })).toBeTruthy();
    const drill = screen.getByRole('button', { name: 'thermostat-01' });
    expect(drill.title).toBe(words.drill);
    fireEvent.click(drill);
    expect(select).toHaveBeenCalledWith({
      slot: 'selectedDevice', binding: { kind: 'device', deviceToken: 'thermostat-01' },
    });
    expect(actions.can).toHaveBeenCalledWith('alarm:write');
  });

  it('localizes acknowledged and cleared states, with cleared taking precedence', () => {
    render(<AlarmTable widget={widget} data={state({ alarms: [
      alarm({ acknowledged: true }),
      alarm({ token: 'alarm-2', state: 'CLEARED', acknowledged: true }),
    ] })} />);
    expect(screen.getByRole('cell', { name: words.acknowledged })).toBeTruthy();
    expect(screen.getByRole('cell', { name: words.cleared })).toBeTruthy();
  });

  it('localizes the loaded empty and loading states', () => {
    const { rerender } = render(<AlarmTable widget={widget} data={state({ alarms: [], total: 0 })} />);
    expect(screen.getByRole('cell', { name: words.empty })).toBeTruthy();
    rerender(<AlarmTable widget={widget} data={state({ alarms: [], total: 0, loading: true })} />);
    expect(screen.getByRole('cell', { name: words.loading })).toBeTruthy();
    expect(screen.queryByText(words.empty)).toBeNull();
  });

  it('localizes action failures and preserves action dispatch tokens', async () => {
    const actions: WidgetActions = {
      can: () => true,
      acknowledgeAlarm: vi.fn().mockRejectedValue(new Error('unavailable')),
      clearAlarm: vi.fn().mockRejectedValue(new Error('unavailable')),
    };
    render(<AlarmTable widget={widget} data={state()} actions={actions} />);
    fireEvent.click(screen.getByRole('button', { name: words.acknowledge }));
    expect(actions.acknowledgeAlarm).toHaveBeenCalledWith('alarm-1');
    expect((await screen.findByRole('img', { name: words.failed })).title).toBe(words.failed);
    fireEvent.click(screen.getByRole('button', { name: words.clear }));
    expect(actions.clearAlarm).toHaveBeenCalledWith('alarm-1');
    expect(await screen.findByRole('img', { name: words.failed })).toBeTruthy();
  });
});
