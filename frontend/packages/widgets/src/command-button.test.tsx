// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { CommandRow, WidgetActions, WidgetInstance } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(cleanup);

import type { CommandStreamState } from './hooks';
import { CommandButton } from './widgets/command-button';

const widget = (options: Record<string, unknown> = {}): WidgetInstance => ({
  id: 'w',
  type: 'command-button',
  layout: { base: { x: 0, y: 0, w: 2, h: 2, z: 1 } },
  datasource: { kind: 'device', deviceToken: 'therm-1', measurements: [] },
  options,
});

const command = (over: Partial<CommandRow> = {}): CommandRow => ({
  token: 'c-1',
  name: 'reboot',
  status: 'SENT',
  payload: null,
  responsePayload: null,
  error: null,
  queuedTime: '2026-07-06T12:00:00Z',
  sentTime: null,
  deliveredTime: null,
  respondedTime: null,
  ...over,
});

const state = (over: Partial<CommandStreamState> = {}): CommandStreamState => ({
  deviceToken: 'therm-1',
  commands: [],
  total: 0,
  loading: false,
  error: null,
  ...over,
});

// A typed sendCommand spy — explicit arg types so the tuple reads in assertions.
type SendSpy = ReturnType<typeof makeSend>;
function makeSend() {
  return vi.fn<(deviceToken: string, name: string, payload?: string) => Promise<{ token: string }>>(
    async () => ({ token: 'dispatch-1' }),
  );
}

// A writable action seam whose sendCommand is the given spy.
function writableActions(sendCommand: SendSpy = makeSend()): WidgetActions {
  return {
    can: () => true,
    acknowledgeAlarm: vi.fn(async () => {}),
    clearAlarm: vi.fn(async () => {}),
    sendCommand,
  };
}

const SCHEMA = JSON.stringify([
  { name: 'delaySeconds', dataType: 'INT', required: true },
  { name: 'force', dataType: 'BOOLEAN' },
]);

describe('CommandButton', () => {
  it('shows a config hint when no command is configured', () => {
    render(<CommandButton widget={widget()} data={state()} actions={writableActions()} />);
    expect(screen.getByText('No command configured.')).toBeTruthy();
  });

  it('renders a typed input per scalar parameter and a Send button', () => {
    render(
      <CommandButton
        widget={widget({ commandName: 'reboot', commandLabel: 'Reboot', parameterSchema: SCHEMA })}
        data={state()}
        actions={writableActions()}
      />,
    );
    // Number input for the INT param (spinbutton), checkbox for BOOLEAN.
    expect(screen.getByRole('spinbutton')).toBeTruthy();
    expect(screen.getByRole('checkbox')).toBeTruthy();
    expect(screen.getByText('Send Reboot')).toBeTruthy();
  });

  it('blocks send and shows an error when a required param is empty', () => {
    const send = makeSend();
    render(
      <CommandButton
        widget={widget({ commandName: 'reboot', parameterSchema: SCHEMA })}
        data={state()}
        actions={writableActions(send)}
      />,
    );
    fireEvent.click(screen.getByRole('button'));
    expect(send).not.toHaveBeenCalled();
    expect(screen.getByText('Required')).toBeTruthy();
  });

  it('issues the command with the coerced JSON payload', () => {
    const send = makeSend();
    render(
      <CommandButton
        widget={widget({ commandName: 'reboot', parameterSchema: SCHEMA })}
        data={state()}
        actions={writableActions(send)}
      />,
    );
    fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '5' } });
    fireEvent.click(screen.getByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: /send/i }));

    expect(send).toHaveBeenCalledTimes(1);
    const [deviceToken, name, payload] = send.mock.calls[0];
    expect(deviceToken).toBe('therm-1');
    expect(name).toBe('reboot');
    expect(JSON.parse(payload!)).toEqual({ delaySeconds: 5, force: true });
  });

  it('issues a parameterless command with no payload', () => {
    const send = makeSend();
    render(
      <CommandButton
        widget={widget({ commandName: 'ping' })}
        data={state()}
        actions={writableActions(send)}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: /send/i }));
    expect(send).toHaveBeenCalledWith('therm-1', 'ping', undefined);
  });

  it('disables Send and explains when the viewer lacks command:write', () => {
    const readOnly: WidgetActions = {
      can: () => false,
      acknowledgeAlarm: vi.fn(async () => {}),
      clearAlarm: vi.fn(async () => {}),
      sendCommand: vi.fn(async () => ({ token: 't' })),
    };
    render(
      <CommandButton widget={widget({ commandName: 'reboot' })} data={state()} actions={readOnly} />,
    );
    expect((screen.getByRole('button', { name: /send/i }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/don’t have permission/i)).toBeTruthy();
  });

  it('disables Send and explains when no device is bound', () => {
    render(
      <CommandButton
        widget={widget({ commandName: 'reboot' })}
        data={state({ deviceToken: null })}
        actions={writableActions()}
      />,
    );
    expect((screen.getByRole('button', { name: /send/i }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Bind a device/i)).toBeTruthy();
  });

  it('lists recent commands with their status', () => {
    render(
      <CommandButton
        widget={widget({ commandName: 'reboot' })}
        data={state({
          commands: [command({ token: 'c-1', name: 'reboot', status: 'SUCCESSFUL' }), command({ token: 'c-2', name: 'calibrate', status: 'FAILED' })],
          total: 2,
        })}
        actions={writableActions()}
      />,
    );
    expect(screen.getByText('Successful')).toBeTruthy();
    expect(screen.getByText('Failed')).toBeTruthy();
    expect(screen.getByText('calibrate')).toBeTruthy();
  });

  it('surfaces a structured (OBJECT) parameter as a non-editable note', () => {
    const schema = JSON.stringify([{ name: 'config', kind: 'OBJECT', parameters: [{ name: 'x' }] }]);
    render(
      <CommandButton
        widget={widget({ commandName: 'configure', parameterSchema: schema })}
        data={state()}
        actions={writableActions()}
      />,
    );
    expect(screen.getByText(/structured parameter/i)).toBeTruthy();
  });
});
