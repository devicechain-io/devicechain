// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command slider and the command toggle: what they send and when, what they refuse to send,
// what they show (the LAST VALUE SENT and its delivery status, never device state), and that a
// viewer without the permission gets a read-only control.

import type { CommandDispatch, CommandRow, WidgetActions, WidgetInstance } from '@devicechain/dashboards';
import type { CommandStreamState } from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { CommandSlider } from './widgets/command-slider';
import { CommandToggle } from './widgets/command-toggle';

afterEach(cleanup);

const widget = (type: 'command-slider' | 'command-toggle', options: Record<string, unknown>): WidgetInstance => ({
  id: 'w',
  type,
  layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 2 } },
  datasource: { kind: 'device', deviceToken: 'therm-1', measurements: [] },
  options,
});

const row = (over: Partial<CommandRow> = {}): CommandRow => ({
  token: 'c-1',
  name: 'setSetpoint',
  status: 'QUEUED',
  payload: JSON.stringify({ target: 21, mode: 'auto' }),
  responsePayload: null,
  error: null,
  queuedTime: '2026-07-06T12:00:00Z',
  sentTime: null,
  respondedTime: null,
  ...over,
});

const state = (commands: CommandRow[] = []): CommandStreamState => ({
  deviceToken: 'therm-1',
  commands,
  total: commands.length,
  loading: false,
  error: null,
});

type SendSpy = ReturnType<typeof makeSend>;
function makeSend(outcome: () => Promise<CommandDispatch> = async () => ({ status: 'sent', token: 'd-1' })) {
  return vi.fn<(deviceToken: string, name: string, payload?: string) => Promise<CommandDispatch>>(outcome);
}
function actionsWith(sendCommand: SendSpy = makeSend(), can = true): WidgetActions {
  return { can: () => can, acknowledgeAlarm: vi.fn(async () => {}), clearAlarm: vi.fn(async () => {}), sendCommand };
}

// The sim's setpoint command: one required bounded DOUBLE and an optional STRING with a default.
const SETPOINT = JSON.stringify([
  { name: 'target', dataType: 'DOUBLE', unit: 'C', required: true, minValue: 15, maxValue: 35 },
  { name: 'mode', dataType: 'STRING', enum: ['heat', 'cool', 'auto'], default: 'auto' },
]);
const slider = (extra: Record<string, unknown> = {}) =>
  widget('command-slider', {
    commandName: 'setSetpoint',
    commandLabel: 'Setpoint',
    parameterSchema: SETPOINT,
    parameter: 'target',
    ...extra,
  });
const input = () => screen.getByRole('slider') as HTMLInputElement;

describe('command-slider', () => {
  it('sends nothing while the thumb is dragged, and one command, with defaults filled, on release', () => {
    const send = makeSend();
    render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(send)} />);
    fireEvent.change(input(), { target: { value: '18' } });
    fireEvent.change(input(), { target: { value: '20' } });
    fireEvent.change(input(), { target: { value: '22' } });
    expect(send).not.toHaveBeenCalled();
    fireEvent.pointerUp(input());
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith('therm-1', 'setSetpoint', '{"target":22,"mode":"auto"}');
  });

  it('sends on Enter for a keyboard user, but not on an arrow key', () => {
    const send = makeSend();
    render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(send)} />);
    fireEvent.change(input(), { target: { value: '19' } });
    fireEvent.keyDown(input(), { key: 'ArrowRight' });
    expect(send).not.toHaveBeenCalled();
    fireEvent.keyDown(input(), { key: 'Enter' });
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][2]).toBe('{"target":19,"mode":"auto"}');
  });

  it('does not send when the thumb was never moved', () => {
    const send = makeSend();
    render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(send)} />);
    fireEvent.pointerUp(input());
    fireEvent.keyDown(input(), { key: 'Enter' });
    expect(send).not.toHaveBeenCalled();
  });

  it('clamps to the command definition range and snaps to the step', () => {
    const send = makeSend();
    render(<CommandSlider widget={slider({ step: 0.5 })} data={state()} actions={actionsWith(send)} />);
    expect(input().min).toBe('15');
    expect(input().max).toBe('35');
    expect(input().step).toBe('0.5');
    fireEvent.change(input(), { target: { value: '99' } });
    fireEvent.pointerUp(input());
    expect(JSON.parse(send.mock.calls[0][2]!).target).toBe(35);
  });

  it('uses authored min and max only where the definition leaves them open', () => {
    const open = JSON.stringify([{ name: 'level', dataType: 'INT', required: true }]);
    render(
      <CommandSlider
        widget={widget('command-slider', { commandName: 'dim', parameterSchema: open, parameter: 'level', min: 0, max: 10 })}
        data={state()}
        actions={actionsWith()}
      />,
    );
    expect(input().min).toBe('0');
    expect(input().max).toBe('10');
    expect(input().step).toBe('1');
  });

  it('refuses to render a range nobody chose', () => {
    const open = JSON.stringify([{ name: 'level', dataType: 'INT', required: true }]);
    render(
      <CommandSlider
        widget={widget('command-slider', { commandName: 'dim', parameterSchema: open, parameter: 'level' })}
        data={state()}
        actions={actionsWith()}
      />,
    );
    expect(screen.queryByRole('slider')).toBeNull();
    expect(screen.getByRole('alert').textContent).toMatch(/min and max/i);
  });

  it('sends one command while one is in flight, however many releases follow', () => {
    const send = makeSend(() => new Promise<CommandDispatch>(() => {}));
    render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(send)} />);
    fireEvent.change(input(), { target: { value: '22' } });
    fireEvent.pointerUp(input());
    fireEvent.change(input(), { target: { value: '25' } });
    fireEvent.pointerUp(input());
    fireEvent.keyDown(input(), { key: 'Enter' });
    expect(send).toHaveBeenCalledTimes(1);
    expect(input().disabled).toBe(true);
  });

  it('refuses to send when another required parameter has no value, and says why', () => {
    const send = makeSend();
    const schema = JSON.stringify([
      { name: 'target', dataType: 'DOUBLE', required: true, minValue: 15, maxValue: 35 },
      { name: 'zone', dataType: 'STRING', required: true },
    ]);
    render(<CommandSlider widget={slider({ parameterSchema: schema })} data={state()} actions={actionsWith(send)} />);
    expect(screen.getByRole('alert').textContent).toMatch(/zone/);
    expect(screen.queryByRole('slider')).toBeNull();
    expect(send).not.toHaveBeenCalled();
  });

  it('refuses a STRING parameter instead of coercing it', () => {
    const schema = JSON.stringify([{ name: 'target', dataType: 'STRING', required: true }]);
    render(<CommandSlider widget={slider({ parameterSchema: schema })} data={state()} actions={actionsWith()} />);
    expect(screen.queryByRole('slider')).toBeNull();
    expect(screen.getByRole('alert').textContent).toMatch(/target/);
  });

  it('refuses a parameter the command does not have, and a missing command', () => {
    const { unmount } = render(
      <CommandSlider widget={slider({ parameter: 'speed' })} data={state()} actions={actionsWith()} />,
    );
    expect(screen.getByRole('alert').textContent).toMatch(/speed/);
    unmount();
    render(<CommandSlider widget={widget('command-slider', {})} data={state()} actions={actionsWith()} />);
    expect(screen.queryByRole('slider')).toBeNull();
    expect(screen.getByRole('alert').textContent).toMatch(/command/i);
  });

  it('will not pick between two numeric parameters', () => {
    const schema = JSON.stringify([
      { name: 'a', dataType: 'INT', minValue: 0, maxValue: 5 },
      { name: 'b', dataType: 'INT', minValue: 0, maxValue: 5 },
    ]);
    render(
      <CommandSlider widget={slider({ parameterSchema: schema, parameter: undefined })} data={state()} actions={actionsWith()} />,
    );
    expect(screen.queryByRole('slider')).toBeNull();
    expect(screen.getByRole('alert').textContent).toMatch(/parameter/i);
  });

  it('is read-only without the command permission, or without a send seam', () => {
    const send = makeSend();
    const { unmount } = render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(send, false)} />);
    expect(input().disabled).toBe(true);
    fireEvent.change(input(), { target: { value: '22' } });
    fireEvent.pointerUp(input());
    fireEvent.keyDown(input(), { key: 'Enter' });
    expect(send).not.toHaveBeenCalled();
    expect(screen.getByText(/permission/i)).toBeTruthy();
    unmount();
    render(<CommandSlider widget={slider()} data={state()} actions={undefined} />);
    expect(input().disabled).toBe(true);
    expect(screen.getByText(/aren.t available/i)).toBeTruthy();
  });

  it('names "last sent", never state, and follows the delivery status', () => {
    const view = render(
      <CommandSlider widget={slider()} data={state([row({ status: 'QUEUED' })])} actions={actionsWith()} />,
    );
    const caption = () => screen.getByRole('status').textContent ?? '';
    expect(caption()).toMatch(/last sent/i);
    expect(caption()).toMatch(/21/);
    expect(caption()).toMatch(/pending/i);
    expect(caption()).not.toMatch(/current|state/i);
    view.rerender(<CommandSlider widget={slider()} data={state([row({ status: 'SENT' })])} actions={actionsWith()} />);
    expect(caption()).toMatch(/pending/i);
    view.rerender(
      <CommandSlider widget={slider()} data={state([row({ status: 'SUCCESSFUL' })])} actions={actionsWith()} />,
    );
    expect(caption()).toMatch(/delivered/i);
    view.rerender(<CommandSlider widget={slider()} data={state([row({ status: 'FAILED' })])} actions={actionsWith()} />);
    expect(caption()).toMatch(/failed/i);
    expect(caption()).not.toMatch(/delivered/i);
  });

  it('reports the newest command of its own name, and nothing before one was sent', () => {
    const rows = [
      row({ token: 'old', status: 'SUCCESSFUL', payload: '{"target":18}', queuedTime: '2026-07-06T11:00:00Z' }),
      row({ token: 'new', status: 'SUCCESSFUL', payload: '{"target":27}', queuedTime: '2026-07-06T12:30:00Z' }),
      row({ token: 'other', name: 'reboot', payload: '{"target":99}', queuedTime: '2026-07-06T13:00:00Z' }),
    ];
    const view = render(<CommandSlider widget={slider()} data={state(rows)} actions={actionsWith()} />);
    expect(screen.getByRole('status').textContent).toMatch(/27/);
    expect(input().value).toBe('27');
    view.rerender(<CommandSlider widget={slider()} data={state()} actions={actionsWith()} />);
    expect(screen.getByRole('status').textContent).toMatch(/nothing sent/i);
  });

  it('shows a Pending last-sent value as soon as the dispatch is accepted', async () => {
    const send = makeSend();
    render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(send)} />);
    fireEvent.change(input(), { target: { value: '24' } });
    await act(async () => {
      fireEvent.pointerUp(input());
    });
    expect(screen.getByRole('status').textContent).toMatch(/24.*pending|pending.*24/i);
  });

  it('shows the server reason for a refusal, and a fixed message for a failure', async () => {
    const refusing = makeSend(async () => ({ status: 'rejected', code: 'invalid', reason: 'target above ceiling' }));
    const { unmount } = render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(refusing)} />);
    fireEvent.change(input(), { target: { value: '30' } });
    await act(async () => {
      fireEvent.pointerUp(input());
    });
    expect(screen.getByRole('alert').textContent).toBe('target above ceiling');
    // A refused value is not "last sent".
    expect(screen.getByRole('status').textContent).toMatch(/nothing sent/i);
    unmount();
    const failing = makeSend(async () => {
      throw new Error('nats: no responders on 10.0.0.7');
    });
    render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(failing)} />);
    fireEvent.change(input(), { target: { value: '30' } });
    await act(async () => {
      fireEvent.pointerUp(input());
    });
    expect(screen.getByRole('alert').textContent).not.toMatch(/nats|10\.0/);
    expect(input().disabled).toBe(false);
  });
});

const BOOL = JSON.stringify([{ name: 'enabled', dataType: 'BOOLEAN', required: true }]);
const toggle = (extra: Record<string, unknown> = {}) =>
  widget('command-toggle', {
    commandName: 'setPower',
    commandLabel: 'Power',
    parameterSchema: BOOL,
    parameter: 'enabled',
    ...extra,
  });
const sw = () => screen.getByRole('switch') as HTMLButtonElement;
const power = (payload: unknown, over: Partial<CommandRow> = {}) =>
  row({ name: 'setPower', payload: JSON.stringify(payload), ...over });

describe('command-toggle', () => {
  it('sends a real boolean, not the string, and nothing before the click', () => {
    const send = makeSend();
    render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith(send)} />);
    expect(send).not.toHaveBeenCalled();
    fireEvent.click(sw());
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0]).toEqual(['therm-1', 'setPower', '{"enabled":true}']);
    expect(JSON.parse(send.mock.calls[0][2]!).enabled).toBe(true);
  });

  it('sends the opposite of the last value sent', () => {
    const send = makeSend();
    render(
      <CommandToggle
        widget={toggle()}
        data={state([power({ enabled: true }, { status: 'SUCCESSFUL' })])}
        actions={actionsWith(send)}
      />,
    );
    expect(sw().getAttribute('aria-checked')).toBe('true');
    fireEvent.click(sw());
    expect(send.mock.calls[0][2]).toBe('{"enabled":false}');
  });

  it('sends once for rapid clicks while one is in flight', () => {
    const send = makeSend(() => new Promise<CommandDispatch>(() => {}));
    render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith(send)} />);
    fireEvent.click(sw());
    fireEvent.click(sw());
    fireEvent.click(sw());
    expect(send).toHaveBeenCalledTimes(1);
    expect(sw().getAttribute('aria-busy')).toBe('true');
  });

  it('can send again once the first dispatch has settled', async () => {
    const send = makeSend();
    render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith(send)} />);
    await act(async () => {
      fireEvent.click(sw());
    });
    await act(async () => {
      fireEvent.click(sw());
    });
    expect(send).toHaveBeenCalledTimes(2);
    // The second click turns it off: the first one's value is what the switch showed.
    expect(send.mock.calls[1][2]).toBe('{"enabled":false}');
  });

  it('includes a defaulted parameter and omits an unset optional one', () => {
    const send = makeSend();
    const schema = JSON.stringify([
      { name: 'enabled', dataType: 'BOOLEAN', required: true },
      { name: 'ramp', dataType: 'INT', default: '5' },
      { name: 'note', dataType: 'STRING' },
      { name: 'quiet', dataType: 'BOOLEAN' },
    ]);
    render(<CommandToggle widget={toggle({ parameterSchema: schema })} data={state()} actions={actionsWith(send)} />);
    fireEvent.click(sw());
    expect(JSON.parse(send.mock.calls[0][2]!)).toEqual({ enabled: true, ramp: 5 });
  });

  it('refuses when another required parameter has no value, including a boolean', () => {
    const send = makeSend();
    const schema = JSON.stringify([
      { name: 'enabled', dataType: 'BOOLEAN', required: true },
      { name: 'confirm', dataType: 'BOOLEAN', required: true },
    ]);
    render(<CommandToggle widget={toggle({ parameterSchema: schema })} data={state()} actions={actionsWith(send)} />);
    expect(screen.getByRole('alert').textContent).toMatch(/confirm/);
    expect(screen.queryByRole('switch')).toBeNull();
    expect(send).not.toHaveBeenCalled();
  });

  it('refuses a numeric parameter, a missing parameter and no command', () => {
    const num = JSON.stringify([{ name: 'enabled', dataType: 'INT', required: true }]);
    const a = render(<CommandToggle widget={toggle({ parameterSchema: num })} data={state()} actions={actionsWith()} />);
    expect(screen.queryByRole('switch')).toBeNull();
    expect(screen.getByRole('alert').textContent).toMatch(/enabled/);
    a.unmount();
    const b = render(<CommandToggle widget={toggle({ parameter: 'nope' })} data={state()} actions={actionsWith()} />);
    expect(screen.getByRole('alert').textContent).toMatch(/nope/);
    b.unmount();
    render(<CommandToggle widget={widget('command-toggle', {})} data={state()} actions={actionsWith()} />);
    expect(screen.queryByRole('switch')).toBeNull();
  });

  it('is read-only without the command permission', () => {
    const send = makeSend();
    render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith(send, false)} />);
    expect(sw().disabled).toBe(true);
    fireEvent.click(sw());
    expect(send).not.toHaveBeenCalled();
    expect(screen.getByText(/permission/i)).toBeTruthy();
  });

  it('shows what was last sent, not the device state, and follows the delivery status', () => {
    const view = render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith()} />);
    const caption = () => screen.getByRole('status').textContent ?? '';
    expect(caption()).toMatch(/nothing sent/i);
    expect(sw().getAttribute('aria-checked')).toBe('false');
    for (const [status, word] of [
      ['QUEUED', /pending/i],
      ['SENT', /pending/i],
      ['SUCCESSFUL', /delivered/i],
      ['TIMEOUT', /failed/i],
    ] as const) {
      view.rerender(
        <CommandToggle widget={toggle()} data={state([power({ enabled: true }, { status })])} actions={actionsWith()} />,
      );
      expect(caption()).toMatch(/last sent/i);
      expect(caption()).toMatch(/\bon\b/i);
      expect(caption()).toMatch(word);
    }
  });

  it('shows the reason for a refusal and keeps the last value it had', async () => {
    const refusing = makeSend(async () => ({
      status: 'rejected',
      code: 'held',
      reason: 'tenant held-command ceiling reached',
    }));
    render(
      <CommandToggle
        widget={toggle()}
        data={state([power({ enabled: false }, { status: 'SUCCESSFUL' })])}
        actions={actionsWith(refusing)}
      />,
    );
    await act(async () => {
      fireEvent.click(sw());
    });
    expect(screen.getByRole('alert').textContent).toBe('tenant held-command ceiling reached');
    expect(sw().getAttribute('aria-checked')).toBe('false');
  });
});
