// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { CommandDispatch, CommandRow, WidgetActions, WidgetInstance } from '@devicechain/dashboards';
import type { CommandStreamState } from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { CommandButton } from './widgets/command-button';
import { validateParams, validateParamsIn } from './widgets/command-params';
import { commandStatusLabel, commandStatusLabelIn } from './widgets/command-status';

const originalLang = document.documentElement.lang;
beforeEach(() => { document.documentElement.lang = 'es-MX'; });
afterEach(() => {
  cleanup();
  document.documentElement.lang = originalLang;
});

const widget: WidgetInstance = {
  id: 'localized-send',
  type: 'command-button',
  layout: { base: { mode: 'grid', col: 0, row: 0, cols: 2, rows: 2 } },
  datasource: { kind: 'device', deviceToken: 'therm-1', measurements: [] },
  options: {
    commandName: 'reboot',
    commandLabel: 'Restart pump',
    parameterSchema: JSON.stringify([
      { name: 'delaySeconds', dataType: 'INT', required: true },
      { name: 'force', dataType: 'BOOLEAN' },
    ]),
  },
};

const row: CommandRow = {
  token: 'previous', name: 'reboot', status: 'SUCCESSFUL', payload: null,
  responsePayload: null, error: null, queuedTime: '2026-07-06T12:00:00Z',
  sentTime: null, respondedTime: null,
};
const data: CommandStreamState = {
  deviceToken: 'therm-1', commands: [row], total: 1, loading: false, error: null,
};

function actions(outcome: () => Promise<CommandDispatch>) {
  const sendCommand = vi.fn<(device: string, name: string, payload?: string) => Promise<CommandDispatch>>(outcome);
  const seam: WidgetActions = {
    can: () => true,
    acknowledgeAlarm: vi.fn(async () => {}),
    clearAlarm: vi.fn(async () => {}),
    sendCommand,
  };
  return { seam, sendCommand };
}

describe('command form locale', () => {
  it('localizes validation, pending, availability failure and history while preserving dispatch data', async () => {
    let reject!: (error: Error) => void;
    const { seam, sendCommand } = actions(() => new Promise((_, fail) => { reject = fail; }));
    render(<CommandButton widget={widget} data={data} actions={seam} />);
    expect(screen.getByText('Exitoso')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Enviar Restart pump' }));
    expect(screen.getByText('Obligatorio')).toBeTruthy();
    expect(sendCommand).not.toHaveBeenCalled();

    fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '5' } });
    fireEvent.click(screen.getByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: 'Enviar Restart pump' }));
    const pending = screen.getByRole('button', { name: 'Enviando…' }) as HTMLButtonElement;
    expect(pending.disabled).toBe(true);
    expect(pending.getAttribute('aria-busy')).toBe('true');
    expect(sendCommand).toHaveBeenCalledExactlyOnceWith('therm-1', 'reboot', JSON.stringify({ delaySeconds: 5, force: true }));

    await act(async () => { reject(new Error('private transport details')); });
    expect(screen.getByRole('alert').textContent).toBe('No se pudo enviar el comando. Inténtalo de nuevo.');
    expect(screen.queryByText(/private transport/)).toBeNull();
    expect((screen.getByRole('button', { name: 'Enviar Restart pump' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('preserves the server refusal under Spanish', async () => {
    const reason = 'parameter "delaySeconds" must be at most 60';
    const { seam } = actions(async () => ({ status: 'rejected', code: 'PAYLOAD_SCHEMA_VIOLATION', reason }));
    render(<CommandButton widget={widget} data={data} actions={seam} />);
    fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '90' } });
    fireEvent.click(screen.getByRole('button', { name: 'Enviar Restart pump' }));
    expect((await screen.findByRole('alert')).textContent).toBe(reason);
  });

  it('uses Spanish permission and history failure messages', () => {
    const { seam } = actions(async () => ({ status: 'sent', token: 'new' }));
    render(<CommandButton widget={widget} data={{ ...data, commands: [], error: new Error('private') }} actions={{ ...seam, can: () => false }} />);
    expect(screen.getByRole('note').textContent).toBe('No tienes permiso para enviar comandos.');
    expect(screen.queryByRole('button')).toBeNull();
    expect(screen.getByText('No se pudo cargar el historial de comandos.')).toBeTruthy();
  });

  it('falls back to English for an unsupported viewer language', () => {
    document.documentElement.lang = 'fr';
    const { seam } = actions(async () => ({ status: 'sent', token: 'new' }));
    render(<CommandButton widget={widget} data={data} actions={seam} />);
    expect(screen.getByRole('button', { name: 'Send Restart pump' })).toBeTruthy();
    expect(screen.getByText('Successful')).toBeTruthy();
    fireEvent.click(screen.getByRole('button'));
    expect(screen.getByText('Required')).toBeTruthy();
  });

  it('localizes helpers implicitly and supports explicit locale overrides', () => {
    expect(commandStatusLabel('SUCCESSFUL')).toBe('Exitoso');
    expect(commandStatusLabelIn('SUCCESSFUL', 'en')).toBe('Successful');
    expect(commandStatusLabelIn('SUCCESSFUL', 'fr')).toBe('Successful');
    expect(commandStatusLabel('FUTURE_STATUS')).toBe('Future status');
    expect(commandStatusLabel('')).toBe('');
    expect(validateParams([{ name: 'x', required: true }], {})).toEqual({ x: 'Obligatorio' });
    expect(validateParamsIn([{ name: 'x', required: true }], {}, 'en')).toEqual({ x: 'Required' });
    expect(validateParamsIn([{ name: 'x', required: true }], {}, 'fr')).toEqual({ x: 'Required' });
    expect(validateParamsIn([{ name: 'x', required: true }], {}, 'ES-es')).toEqual({ x: 'Obligatorio' });
  });

  it.each([
    ['QUEUED', 'En cola'], ['SENT', 'Enviado'], ['HELD', 'Retenido'], ['PARKED', 'En espera'],
    ['SUCCESSFUL', 'Exitoso'], ['FAILED', 'Fallido'], ['TIMEOUT', 'Tiempo agotado'],
    ['EXPIRED', 'Caducado'], ['CANCELLED', 'Cancelado'],
  ])('uses console vocabulary for %s', (status, label) => {
    expect(commandStatusLabel(status)).toBe(label);
    expect(commandStatusLabelIn(status, 'es')).toBe(label);
  });

  it('localizes numeric and structured validation without changing field keys', () => {
    expect(validateParams([
      { name: 'numeric', dataType: 'DOUBLE' },
      { name: 'integer', dataType: 'INT' },
      { name: 'lower', dataType: 'INT', minValue: 2 },
      { name: 'upper', dataType: 'INT', maxValue: 4 },
      { name: 'nested', kind: 'OBJECT', required: true },
    ], { numeric: 'bad', integer: '1.5', lower: '1', upper: '5' })).toEqual({
      numeric: 'Debe ser un número', integer: 'Debe ser un número entero',
      lower: 'Debe ser ≥ 2', upper: 'Debe ser ≤ 4',
      nested: 'Parámetro estructurado — no compatible aquí',
    });
  });
});
