// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the command slider and toggle share beyond the pure logic: the send itself (one at a time,
// the permission gate, the refusal's reason), the "last sent" caption with its delivery status, and
// the explained empty state. Neither control shows device state: the caption names what was SENT.

import type { CommandDispatch, WidgetActions } from '@devicechain/dashboards';
import { useCallback, useEffect, useRef, useState, type CSSProperties } from 'react';

import { css, fontVar, spaceVar } from '@devicechain/widget-sdk';

import { controlStrings, type ControlStrings } from './command-control-strings';
import { deliveryGroup, type LastSent, type Optimistic, type Refusal } from './command-control-core';
import { commandStatusColor, commandStatusLabel } from './widgets/command-status';

// The smallest hit area a touch control may have, in CSS px.
export const TOUCH_TARGET_PX = 44;

export function refusalText(r: Refusal, s: ControlStrings, expect: 'number' | 'boolean'): string {
  switch (r.code) {
    case 'noCommand':
      return s.noCommand;
    case 'setParameter':
      return s.setParameter;
    case 'parameterMissing':
      return s.parameterMissing(r.name);
    case 'wrongType':
      return s.wrongType(r.name, expect === 'number' ? s.numberKind : s.booleanKind);
    case 'fixedChoices':
      return s.fixedChoices(r.name);
    case 'otherRequired':
      return s.otherRequired(r.names);
    case 'setRange':
      return s.setRange;
    case 'badRange':
      return s.badRange;
  }
}

export interface CommandSender {
  strings: ControlStrings;
  // Why the control is read-only, or undefined when the viewer may send.
  readOnlyReason: string | undefined;
  canSend: boolean;
  // A dispatch is in flight.
  pending: boolean;
  error: string | null;
  // A send the platform accepted, until the history shows it.
  optimistic: Optimistic | null;
  // Returns whether a command was dispatched. At most one is in flight at a time.
  send: (payload: string, value: number | boolean) => boolean;
}

// useCommandSender owns one control's sending. `resetKey` identifies the configured command: when
// it changes, an in-flight send's result is dropped rather than landing under the new command.
export function useCommandSender(
  actions: WidgetActions | undefined,
  deviceToken: string | null | undefined,
  commandName: string | undefined,
  resetKey: string,
): CommandSender {
  const strings = controlStrings();
  const canWrite = actions?.can('command:write') ?? false;
  const hasSeam = typeof actions?.sendCommand === 'function';
  const canSend = canWrite && hasSeam && !!deviceToken && !!commandName;

  // The in-flight flag is a ref, not state: two clicks in the same tick both see the state before
  // either has re-rendered, and only a ref closes that window.
  const inFlight = useRef(false);
  const generation = useRef(0);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [optimistic, setOptimistic] = useState<Optimistic | null>(null);

  useEffect(() => {
    generation.current += 1;
    inFlight.current = false;
    setPending(false);
    setError(null);
    setOptimistic(null);
  }, [resetKey]);

  const send = useCallback(
    (payload: string, value: number | boolean): boolean => {
      if (!canSend || inFlight.current || !actions?.sendCommand || !deviceToken || !commandName) return false;
      const gen = generation.current;
      inFlight.current = true;
      setPending(true);
      setError(null);
      const settle = () => {
        if (gen !== generation.current) return;
        inFlight.current = false;
        setPending(false);
      };
      let promise: Promise<CommandDispatch>;
      try {
        promise = actions.sendCommand(deviceToken, commandName, payload);
      } catch {
        setError(strings.sendFailed);
        settle();
        return true;
      }
      promise
        .then((dispatch) => {
          if (gen !== generation.current) return;
          // A refusal is shown with the server's reason (it is client-safe and the operator can act
          // on it); a thrown error is an availability failure whose text is internal, so it gets
          // one fixed message instead. A refused value is not "last sent".
          if (dispatch.status === 'rejected') setError(dispatch.reason);
          else setOptimistic({ token: dispatch.token, value });
        })
        .catch(() => {
          if (gen === generation.current) setError(strings.sendFailed);
        })
        .finally(settle);
      return true;
    },
    [actions, canSend, commandName, deviceToken, strings.sendFailed],
  );

  // A view with no send seam at all (a read-only embed) is told so, rather than blamed on the
  // viewer's permissions.
  const readOnlyReason = !hasSeam
    ? strings.noSeam
    : !canWrite
      ? strings.noPermission
      : !deviceToken
        ? strings.noDevice
        : undefined;
  return { strings, readOnlyReason, canSend, pending, error, optimistic, send };
}

const note: CSSProperties = {
  fontSize: fontVar('xs'),
  lineHeight: 1.25,
  color: css('muted-foreground'),
  overflowWrap: 'anywhere',
};

// The explained empty state: why the control has nothing to offer, as an alert so it is announced.
export function ControlEmpty({ message }: { message: string }) {
  return (
    <div
      role="alert"
      data-control-empty=""
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        height: '100%',
        textAlign: 'center',
        color: css('muted-foreground'),
        fontSize: fontVar('sm'),
        overflowWrap: 'anywhere',
      }}
    >
      {message}
    </div>
  );
}

// The notes under a control: the reason it is read-only, and the last send's error.
export function ControlNotes({ sender }: { sender: CommandSender }) {
  return (
    <>
      {sender.readOnlyReason ? <div style={note}>{sender.readOnlyReason}</div> : null}
      {sender.error ? (
        <div role="alert" style={{ ...note, color: commandStatusColor('FAILED') }}>
          {sender.error}
        </div>
      ) : null}
    </>
  );
}

// "Last sent: 22 C · Pending". The words are the viewer's: pending, delivered or failed, with the
// precise status on hover. `override` replaces the whole line (the slider's "not sent yet" while a
// value is staged), so the caption never claims a staged value was sent.
export function LastSentCaption({
  id,
  last,
  format,
  strings,
  override,
  truncated = false,
}: {
  id: string;
  last: LastSent | null;
  format: (value: number | boolean | undefined) => string;
  strings: ControlStrings;
  override?: string;
  // The device has more commands than the page read, so an empty match proves nothing.
  truncated?: boolean;
}) {
  const group = last ? deliveryGroup(last.status) : undefined;
  const word =
    group === 'pending'
      ? strings.pending
      : group === 'delivered'
        ? strings.delivered
        : group === 'failed'
          ? strings.failed
          : last
            ? commandStatusLabel(last.status)
            : '';
  return (
    <div
      id={id}
      role="status"
      aria-live="polite"
      data-last-sent=""
      data-delivery={group}
      style={{ ...note, fontSize: fontVar('sm'), display: 'flex', flexWrap: 'wrap', alignItems: 'center', columnGap: spaceVar(1) }}
    >
      {override ? (
        <span>{override}</span>
      ) : last ? (
        <>
          <span>{strings.lastSent}:</span>
          <span style={{ color: css('foreground'), fontWeight: 600 }}>{format(last.value)}</span>
          <span aria-hidden="true">·</span>
          <span
            title={last.status}
            style={{ display: 'inline-flex', alignItems: 'center', gap: 4, color: commandStatusColor(last.status), fontWeight: 600 }}
          >
            <span aria-hidden="true" style={{ width: 7, height: 7, borderRadius: '50%', background: commandStatusColor(last.status) }} />
            {word}
          </span>
        </>
      ) : (
        <span>{truncated ? strings.beyondHistory : strings.nothingSent}</span>
      )}
    </div>
  );
}
