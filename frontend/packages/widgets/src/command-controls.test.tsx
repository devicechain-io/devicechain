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
    // In flight it is aria-disabled and ignores input, but stays focusable: `disabled` would
    // throw a keyboard user's focus onto the page body right after Enter.
    expect(input().getAttribute('aria-disabled')).toBe('true');
    expect(input().disabled).toBe(false);
    expect(input().dataset.staged).toBeUndefined();
  });

  it('tells a keyboard user that Enter sends, and voices the staged value', () => {
    render(<CommandSlider widget={slider()} data={state([row({ status: 'SUCCESSFUL' })])} actions={actionsWith()} />);
    const described = (input().getAttribute('aria-describedby') ?? '').split(' ');
    const texts = described.map((id) => document.getElementById(id)?.textContent ?? '');
    expect(texts.join(' ')).toMatch(/press Enter to send/i);
    expect(input().getAttribute('aria-valuetext')).toBe('21 C');
    fireEvent.change(input(), { target: { value: '23' } });
    expect(input().getAttribute('aria-valuetext')).toBe('23 C');
    expect(screen.getByRole('status').textContent).toMatch(/not sent yet/i);
  });

  // 🔴 A staged value that was not committed is DISCARDED, never sent by a later stray release.
  for (const [how, abandon] of [
    ['the pointer is cancelled', () => fireEvent.pointerCancel(input())],
    ['the pointer capture is lost mid-drag', () => fireEvent.lostPointerCapture(input())],
    ['the pointer is released off the slider', () => fireEvent.pointerUp(document.body)],
    ['Escape is pressed', () => fireEvent.keyDown(input(), { key: 'Escape' })],
    ['focus leaves the slider', () => fireEvent.blur(input())],
  ] as const) {
    it(`discards the staged value, and sends nothing, when ${how}`, () => {
      const send = makeSend();
      render(<CommandSlider widget={slider()} data={state([row({ status: 'SUCCESSFUL' })])} actions={actionsWith(send)} />);
      fireEvent.pointerDown(input());
      fireEvent.change(input(), { target: { value: '30' } });
      expect(input().value).toBe('30');
      abandon();
      expect(input().value).toBe('21');
      expect(input().dataset.staged).toBeUndefined();
      expect(screen.getByRole('status').textContent).toMatch(/last sent.*21/i);
      // Nothing is left behind for a later Enter or release to send.
      fireEvent.keyDown(input(), { key: 'Enter' });
      fireEvent.pointerUp(input());
      expect(send).not.toHaveBeenCalled();
    });
  }

  it('a capture lost AFTER the release on the slider does not undo the send', () => {
    const send = makeSend();
    render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(send)} />);
    fireEvent.pointerDown(input());
    fireEvent.change(input(), { target: { value: '26' } });
    fireEvent.pointerUp(input());
    fireEvent.lostPointerCapture(input());
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][2]).toBe('{"target":26,"mode":"auto"}');
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

  it('shows a short View only caption and carries the full reason as the description', () => {
    const check = (el: HTMLElement) => {
      const caption = document.querySelector('[data-read-only]') as HTMLElement;
      expect(caption.textContent).toBe('View only');
      const ids = (el.getAttribute('aria-describedby') ?? '').split(' ');
      const described = ids.map((id) => document.getElementById(id)?.textContent ?? '').join(' ');
      expect(described).toMatch(/permission to send commands/);
      expect(caption.getAttribute('title')).toMatch(/permission to send commands/);
      // The long sentence is never the visible text.
      expect(caption.textContent).not.toMatch(/permission/);
    };
    const s = render(<CommandSlider widget={slider()} data={state()} actions={actionsWith(makeSend(), false)} />);
    check(input());
    s.unmount();
    render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith(makeSend(), false)} />);
    check(document.querySelector('[aria-describedby]') as HTMLElement);
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
    expect(screen.getByRole('status').textContent).toMatch(/nothing sent/i);    // A history longer than the page, with none of this command on it, proves nothing was sent
    // only within the page: it says so rather than "nothing sent".
    view.rerender(
      <CommandSlider widget={slider()} data={{ ...state([row({ name: 'reboot' })]), total: 80 }} actions={actionsWith()} />,
    );
    expect(screen.getByRole('status').textContent).toMatch(/not among the recent commands/i);
    expect(screen.getByRole('status').textContent).not.toMatch(/nothing sent/i);
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

  it('does not wait on delivery: a second send while the first is still pending is the one shown', async () => {
    // Blocking until delivery would lock the control for a device that is away; only the dispatch
    // itself is guarded. The newer send must then be what "Last sent" reports.
    const send = makeSend(async () => ({ status: 'sent', token: 'd-2' }));
    render(
      <CommandSlider widget={slider()} data={state([row({ token: 'c-1', status: 'SENT' })])} actions={actionsWith(send)} />,
    );
    expect(screen.getByRole('status').textContent).toMatch(/21.*pending/i);
    fireEvent.change(input(), { target: { value: '25' } });
    await act(async () => {
      fireEvent.pointerUp(input());
    });
    expect(send).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('status').textContent).toMatch(/25.*pending/i);
    expect(screen.getByRole('status').textContent).not.toMatch(/21/);
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
const onButton = () => screen.getByRole('button', { name: /^on$/i });
const power = (payload: unknown, over: Partial<CommandRow> = {}) =>
  row({ name: 'setPower', payload: JSON.stringify(payload), ...over });

describe('command-slider, stepper mode', () => {
  const stepper = (extra: Record<string, unknown> = {}) => slider({ mode: 'stepper', step: 1, ...extra });
  const spin = () => screen.getByRole('spinbutton');
  const press = (which: 'up' | 'down' | 'send') => fireEvent.click(document.querySelector(`[data-step="${which}"]`)!);
  const num = () => Number(spin().getAttribute('aria-valuenow'));

  it('leaves the slider alone: no mode, an unknown mode and mode slider all draw the range input', () => {
    for (const extra of [{}, { mode: 'slider' }, { mode: 'bogus' }]) {
      render(<CommandSlider widget={slider(extra)} data={state()} actions={actionsWith()} />);
      expect(screen.getByRole('slider')).toBeTruthy();
      expect(screen.queryByRole('spinbutton')).toBeNull();
      cleanup();
    }
  });

  it('takes its bounds from the command parameter and exposes spinbutton semantics', () => {
    render(<CommandSlider widget={stepper()} data={state()} actions={actionsWith()} />);
    expect(spin().getAttribute('aria-valuemin')).toBe('15');
    expect(spin().getAttribute('aria-valuemax')).toBe('35');
    expect(spin().getAttribute('aria-label')).toBe('Setpoint');
    expect(screen.queryByRole('slider')).toBeNull();
  });

  it('clamps at min and max and never steps past them', () => {
    render(<CommandSlider widget={stepper({ step: 5 })} data={state([row({ status: 'SUCCESSFUL' })])} actions={actionsWith()} />);
    expect(num()).toBe(20); // 21 snapped onto the 5-step grid from min
    for (let i = 0; i < 6; i++) press('up');
    expect(num()).toBe(35);
    for (let i = 0; i < 9; i++) press('down');
    expect(num()).toBe(15);
  });

  it('steps by 0.1 without float drift', () => {
    const schema = JSON.stringify([{ name: 'target', dataType: 'DOUBLE', required: true, minValue: 0, maxValue: 1 }]);
    render(
      <CommandSlider
        widget={widget('command-slider', { commandName: 'setSetpoint', parameterSchema: schema, parameter: 'target', mode: 'stepper', step: 0.1 })}
        data={state()}
        actions={actionsWith()}
      />,
    );
    press('up');
    press('up');
    press('up');
    expect(num()).toBe(0.3);
    expect(spin().textContent).toBe('0.3');
  });

  it('sends nothing on a press, and one command with defaults on Send', () => {
    const send = makeSend();
    render(<CommandSlider widget={stepper()} data={state()} actions={actionsWith(send)} />);
    press('up');
    press('up');
    expect(send).not.toHaveBeenCalled();
    expect(screen.getByRole('status').textContent).toMatch(/Press Send/);
    press('send');
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith('therm-1', 'setSetpoint', '{"target":17,"mode":"auto"}');
    press('send');
    expect(send).toHaveBeenCalledTimes(1);
  });

  it('Send does nothing before a value is staged', () => {
    const send = makeSend();
    render(<CommandSlider widget={stepper()} data={state()} actions={actionsWith(send)} />);
    press('send');
    expect(send).not.toHaveBeenCalled();
  });

  it('a disruptive command is still only sent by the explicit Send, and shows as held while the platform holds it', () => {
    const send = makeSend();
    const held = row({ status: 'HELD', payload: JSON.stringify({ target: 25, mode: 'auto' }) });
    render(<CommandSlider widget={stepper()} data={state([held])} actions={actionsWith(send)} />);
    expect(screen.getByRole('status').textContent).toMatch(/Last sent.*25.*Pending/);
    press('down');
    expect(send).not.toHaveBeenCalled();
    fireEvent.keyDown(spin(), { key: 'Escape' });
    expect(num()).toBe(25);
    press('up');
    press('send');
    expect(send).toHaveBeenCalledTimes(1);
  });

  it('follows the delivery status after a send', async () => {
    render(<CommandSlider widget={stepper()} data={state()} actions={actionsWith(makeSend())} />);
    press('up');
    press('send');
    await act(async () => {});
    expect(screen.getByRole('status').textContent).toMatch(/Last sent.*16.*Pending/);
    cleanup();
    render(<CommandSlider widget={stepper()} data={state([row({ status: 'FAILED' })])} actions={actionsWith()} />);
    expect(screen.getByRole('status').textContent).toMatch(/Failed/);
  });

  it('answers the arrows, Page and Home/End, sends on Enter and discards on Escape', () => {
    const send = makeSend();
    render(<CommandSlider widget={stepper()} data={state([row({ status: 'SUCCESSFUL' })])} actions={actionsWith(send)} />);
    fireEvent.keyDown(spin(), { key: 'ArrowUp' });
    expect(num()).toBe(22);
    fireEvent.keyDown(spin(), { key: 'ArrowLeft' });
    fireEvent.keyDown(spin(), { key: 'ArrowDown' });
    expect(num()).toBe(20);
    fireEvent.keyDown(spin(), { key: 'PageUp' });
    expect(num()).toBe(30);
    fireEvent.keyDown(spin(), { key: 'PageUp' });
    expect(num()).toBe(35);
    fireEvent.keyDown(spin(), { key: 'PageDown' });
    expect(num()).toBe(25);
    fireEvent.keyDown(spin(), { key: 'Home' });
    expect(num()).toBe(15);
    fireEvent.keyDown(spin(), { key: 'End' });
    expect(num()).toBe(35);
    fireEvent.keyDown(spin(), { key: 'Escape' });
    expect(num()).toBe(21);
    expect(send).not.toHaveBeenCalled();
    fireEvent.keyDown(spin(), { key: 'ArrowUp' });
    fireEvent.keyDown(spin(), { key: 'Enter' });
    expect(send).toHaveBeenCalledTimes(1);
    expect(JSON.parse(send.mock.calls[0][2]!).target).toBe(22);
  });

  it('is inert without the command permission', () => {
    const send = makeSend();
    render(<CommandSlider widget={stepper()} data={state()} actions={actionsWith(send, false)} />);
    press('up');
    fireEvent.keyDown(spin(), { key: 'ArrowUp' });
    expect(spin().getAttribute('aria-disabled')).toBe('true');
    expect(spin().getAttribute('data-staged')).toBeNull();
    press('send');
    expect(send).not.toHaveBeenCalled();
  });

  it('refuses a range nobody chose, as the slider does', () => {
    const open = JSON.stringify([{ name: 'level', dataType: 'INT', required: true }]);
    render(
      <CommandSlider
        widget={widget('command-slider', { commandName: 'dim', parameterSchema: open, parameter: 'level', mode: 'stepper' })}
        data={state()}
        actions={actionsWith()}
      />,
    );
    expect(screen.getByRole('alert').textContent).toMatch(/min and max/);
    expect(screen.queryByRole('spinbutton')).toBeNull();
  });

  it('sends a whole number for an INT parameter, whatever the authored step', () => {
    const send = makeSend();
    const schema = JSON.stringify([{ name: 'level', dataType: 'INT', required: true, minValue: 0, maxValue: 10 }]);
    render(
      <CommandSlider
        widget={widget('command-slider', { commandName: 'dim', parameterSchema: schema, parameter: 'level', mode: 'stepper', step: 0.5 })}
        data={state()}
        actions={actionsWith(send)}
      />,
    );
    press('up');
    expect(num()).toBe(1);
    press('send');
    expect(send).toHaveBeenCalledWith('therm-1', 'dim', '{"level":1}');
  });

  it('says there is no value, rather than announcing the minimum, before anything was sent', () => {
    render(<CommandSlider widget={stepper()} data={state()} actions={actionsWith()} />);
    expect(spin().textContent).toBe('—');
    expect(spin().getAttribute('aria-valuenow')).toBeNull();
    expect(spin().getAttribute('aria-valuetext')).toBe('no value');
    press('up');
    expect(spin().getAttribute('aria-valuetext')).toBe('16 C');
  });

  it('keeps Send in the tab order, and the − / + out of it (the arrows on the value are their keys)', () => {
    render(<CommandSlider widget={stepper()} data={state()} actions={actionsWith()} />);
    const at = (w: string) => document.querySelector(`[data-step="${w}"]`)!;
    expect(at('send').getAttribute('tabindex')).toBeNull();
    expect(at('send').tagName).toBe('BUTTON');
    expect(at('up').getAttribute('tabindex')).toBe('-1');
    expect(spin().getAttribute('tabindex')).toBe('0');
  });

  it('discards a staged value when a newer command arrives, so a stale Send cannot overwrite it', () => {
    const send = makeSend();
    const { rerender } = render(<CommandSlider widget={stepper()} data={state([row({ status: 'SUCCESSFUL' })])} actions={actionsWith(send)} />);
    press('up');
    expect(num()).toBe(22);
    // A status update on the same command is not a newer command: the staged value stays.
    rerender(<CommandSlider widget={stepper()} data={state([row({ status: 'SUCCESSFUL', respondedTime: '2026-07-06T12:00:05Z' })])} actions={actionsWith(send)} />);
    expect(spin().getAttribute('data-staged')).toBe('true');
    const newer = row({ token: 'c-2', status: 'QUEUED', queuedTime: '2026-07-06T12:01:00Z', payload: JSON.stringify({ target: 30, mode: 'auto' }) });
    rerender(<CommandSlider widget={stepper()} data={state([row({ status: 'SUCCESSFUL' }), newer])} actions={actionsWith(send)} />);
    expect(spin().getAttribute('data-staged')).toBeNull();
    expect(num()).toBe(30);
    press('send');
    expect(send).not.toHaveBeenCalled();
  });
});

describe('command-toggle', () => {
  it('sends a real boolean, not the string, and nothing before the click', () => {
    const send = makeSend();
    render(<CommandToggle widget={toggle()} data={state([power({ enabled: true }, { status: 'SUCCESSFUL' })])} actions={actionsWith(send)} />);
    expect(send).not.toHaveBeenCalled();
    fireEvent.click(sw());
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0]).toEqual(['therm-1', 'setPower', '{"enabled":false}']);
    expect(JSON.parse(send.mock.calls[0][2]!).enabled).toBe(false);
  });

  // 🔴 A toggle never guesses which way to flip. Each of these leaves it not knowing the live
  // intent, so it offers both values and sends exactly the one chosen.
  for (const [why, rows, total] of [
    ['nothing was sent', [], 0],
    ['the history is longer than the page and holds none of this command', [row({ name: 'reboot' })], 80],
    ['the newest command FAILED: its value never reached the device', [power({ enabled: true }, { status: 'FAILED' })], 1],
    ['the newest command timed out', [power({ enabled: false }, { status: 'TIMEOUT' })], 1],
    ['the newest command carries no readable value', [power({ enabled: 'yes' }, { status: 'SUCCESSFUL' })], 1],
    ['the newest status is not one this control knows', [power({ enabled: true }, { status: 'MYSTERY' })], 1],
  ] as const) {
    it(`asks On or Off, never flips, when ${why}`, () => {
      for (const [word, sent] of [
        [/^on$/i, '{"enabled":true}'],
        [/^off$/i, '{"enabled":false}'],
      ] as const) {
        const send = makeSend();
        const data = { ...state([...rows]), total };
        render(<CommandToggle widget={toggle()} data={data} actions={actionsWith(send)} />);
        expect(screen.queryByRole('switch')).toBeNull();
        const group = screen.getByRole('group', { name: 'Power' });
        expect(group.querySelectorAll('button')).toHaveLength(2);
        fireEvent.click(screen.getByRole('button', { name: word }));
        expect(send).toHaveBeenCalledTimes(1);
        expect(send.mock.calls[0][2]).toBe(sent);
        cleanup();
      }
    });
  }

  it('still flips from a value that is pending: the operator issued it and it is on its way', () => {
    const send = makeSend();
    render(<CommandToggle widget={toggle()} data={state([power({ enabled: true }, { status: 'SENT' })])} actions={actionsWith(send)} />);
    expect(sw().getAttribute('aria-checked')).toBe('true');
    fireEvent.click(sw());
    expect(send.mock.calls[0][2]).toBe('{"enabled":false}');
  });

  it('a newer FAILED command wins over an older delivered one: no flip from the older value', () => {
    const send = makeSend();
    const rows = [
      power({ enabled: false }, { token: 'new', status: 'FAILED', queuedTime: '2026-07-06T12:30:00Z' }),
      power({ enabled: true }, { token: 'old', status: 'SUCCESSFUL', queuedTime: '2026-07-06T11:00:00Z' }),
    ];
    render(<CommandToggle widget={toggle()} data={state(rows)} actions={actionsWith(send)} />);
    expect(screen.queryByRole('switch')).toBeNull();
    expect(screen.getByRole('status').textContent).toMatch(/off.*failed/i);
  });

  it('the choice is read-only without the command permission, too', () => {
    const send = makeSend();
    render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith(send, false)} />);
    for (const b of screen.getByRole('group').querySelectorAll('button')) {
      expect((b as HTMLButtonElement).disabled).toBe(true);
      fireEvent.click(b);
    }
    expect(send).not.toHaveBeenCalled();
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
    render(<CommandToggle widget={toggle()} data={state([power({ enabled: true }, { status: 'SUCCESSFUL' })])} actions={actionsWith(send)} />);
    fireEvent.click(sw());
    fireEvent.click(sw());
    fireEvent.click(sw());
    expect(send).toHaveBeenCalledTimes(1);
    expect(sw().getAttribute('aria-busy')).toBe('true');
  });

  it('can send again once the first dispatch has settled', async () => {
    const send = makeSend();
    render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith(send)} />);
    // Nothing sent: the operator chooses On. Once that is accepted it is a pending intent, so the
    // switch appears, on, and the next click flips it.
    await act(async () => {
      fireEvent.click(onButton());
    });
    expect(sw().getAttribute('aria-checked')).toBe('true');
    await act(async () => {
      fireEvent.click(sw());
    });
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[0][2]).toBe('{"enabled":true}');
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
    fireEvent.click(onButton());
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
    render(
      <CommandToggle widget={toggle()} data={state([power({ enabled: true }, { status: 'SUCCESSFUL' })])} actions={actionsWith(send, false)} />,
    );
    expect(sw().disabled).toBe(true);
    fireEvent.click(sw());
    expect(send).not.toHaveBeenCalled();
    expect(screen.getByText(/permission/i)).toBeTruthy();
  });

  it('shows what was last sent, not the device state, and follows the delivery status', () => {
    const view = render(<CommandToggle widget={toggle()} data={state()} actions={actionsWith()} />);
    const caption = () => screen.getByRole('status').textContent ?? '';
    expect(caption()).toMatch(/nothing sent/i);
    expect(screen.queryByRole('switch')).toBeNull();
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
