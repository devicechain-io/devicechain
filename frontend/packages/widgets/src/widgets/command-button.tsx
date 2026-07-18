// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// command-button — issue a device command from the dashboard and watch its delivery
// lifecycle (command-delivery). The console bakes a command definition into the widget's
// options at author time (its name + parameterSchema); this renders a typed form from
// that schema, issues via the action seam (gated on command:write), and shows the recent
// commands for the target device with live status (QUEUED → SENT → DELIVERED →
// SUCCESSFUL / FAILED …), so an operator can see whether a command was acted on rather
// than only that it was sent. Bound through the hub's control channel, so it renders identically from live
// data or the synthetic preview source.
//
// The baked schema is a SNAPSHOT taken when the author picked the command: a later edit
// to the profile's command definition (a changed schema, or a deleted command) leaves
// this widget's form stale until the author re-picks it. That's acceptable — dashboards
// are opaque snapshots — and a since-removed command fails visibly at the delivery
// boundary (the server validates device existence and JSON); it isn't silently wrong.

import type { CommandParameter } from '@devicechain/dashboards';
import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';

import type { CommandStreamState } from '../hooks';
import { formatDateTime } from '../format';
import { WidgetFrame } from '../frame';
import { css } from '../theme';
import { optString, type WidgetProps } from '../widget';
import { commandStatusColor, commandStatusLabel } from './command-status';
import {
  buildPayload,
  defaultValues,
  isScalar,
  parseBool,
  parseParameterSchema,
  validateParams,
} from './command-params';

export function CommandButton({ widget, data, actions }: WidgetProps<CommandStreamState>) {
  const commandName = optString(widget.options, 'commandName');
  const commandLabel = optString(widget.options, 'commandLabel') ?? commandName;
  const parameterSchema = optString(widget.options, 'parameterSchema') ?? null;

  const params = useMemo(() => parseParameterSchema(parameterSchema), [parameterSchema]);
  // A stable key over the configured command: reset the form when the author repoints
  // the widget at a different command (or edits its schema).
  const schemaKey = `${commandName ?? ''}::${parameterSchema ?? ''}`;

  const [values, setValues] = useState<Record<string, string>>(() => defaultValues(params));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [pending, setPending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [sentToken, setSentToken] = useState<string | null>(null);
  // Bumped whenever the configured command changes; an in-flight send captures the
  // current value and drops its result if it settles after a repoint (so command A's
  // dispatch token/error can't land under a freshly-configured command B).
  const sendGen = useRef(0);

  // Reset the form to the schema's defaults whenever the configured command changes.
  useEffect(() => {
    sendGen.current += 1;
    setValues(defaultValues(params));
    setErrors({});
    setSendError(null);
    setSentToken(null);
    // params is derived from schemaKey; keying on schemaKey avoids re-running on an
    // equal-but-new params array reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [schemaKey]);

  const canWrite = actions?.can('command:write') ?? false;
  const hasSeam = typeof actions?.sendCommand === 'function';
  const deviceToken = data.deviceToken;
  // The form is operable only when the viewer may write AND the runtime supplies the send
  // seam; a read-only mount shows the form disabled. Sending additionally needs a bound
  // target device and a configured command.
  const formDisabled = !canWrite || !hasSeam || pending;
  const canSend = !formDisabled && !!deviceToken && !!commandName;

  const setField = (name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }));
    // Clear a field's error as the operator edits it.
    setErrors((prev) => (prev[name] ? { ...prev, [name]: '' } : prev));
    setSendError(null);
  };

  const send = () => {
    if (!deviceToken || !commandName || !actions?.sendCommand) return;
    const found = validateParams(params, values);
    const active = Object.fromEntries(Object.entries(found).filter(([, v]) => v));
    if (Object.keys(active).length > 0) {
      setErrors(active);
      return;
    }
    const payload = buildPayload(params, values);
    const gen = sendGen.current; // capture: drop the result if the command is repointed mid-flight
    setPending(true);
    setSendError(null);
    // Call synchronously so a dispatch is observable, but guard a synchronous throw so a
    // misbehaving seam can't leave the button stuck-disabled.
    let promise: Promise<{ token: string }>;
    try {
      promise = actions.sendCommand(deviceToken, commandName, payload);
    } catch (err) {
      setSendError(errText(err));
      setPending(false);
      return;
    }
    promise
      .then((dispatch) => {
        if (gen === sendGen.current) setSentToken(dispatch.token);
      })
      .catch((err) => {
        if (gen === sendGen.current) setSendError(errText(err));
      })
      .finally(() => {
        if (gen === sendGen.current) setPending(false);
      });
  };

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%', gap: 10 }}>
        {!commandName ? (
          <div style={{ ...hint, margin: 'auto' }}>No command configured.</div>
        ) : (
          <>
            {/* Typed parameter form. */}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              {params.map((param) =>
                isScalar(param) ? (
                  <ParamField
                    key={param.name}
                    widgetId={widget.id}
                    param={param}
                    value={values[param.name] ?? ''}
                    error={errors[param.name]}
                    disabled={formDisabled}
                    onChange={(v) => setField(param.name, v)}
                  />
                ) : (
                  // A structured (OBJECT) parameter isn't form-editable. A REQUIRED one
                  // blocks sending (validateParams flags it); flag that in the note.
                  <div key={param.name} style={{ ...hint, color: param.required ? commandStatusColor('FAILED') : css('muted-foreground') }}>
                    {param.name}: {param.required ? 'required ' : ''}structured parameter — not supported here.
                  </div>
                ),
              )}

              <button
                type="button"
                style={{ ...sendButton, opacity: canSend ? 1 : 0.55, cursor: canSend ? 'pointer' : 'not-allowed' }}
                disabled={!canSend}
                aria-busy={pending}
                onClick={send}
              >
                {pending ? 'Sending…' : `Send ${commandLabel}`}
              </button>

              {!canWrite ? (
                <div style={hint}>You don’t have permission to issue commands.</div>
              ) : !hasSeam ? (
                <div style={hint}>Commands aren’t available in this view.</div>
              ) : !deviceToken ? (
                <div style={hint}>No device is selected for this command.</div>
              ) : null}
              {sendError ? (
                <div role="alert" style={{ ...hint, color: commandStatusColor('FAILED') }}>
                  {sendError}
                </div>
              ) : null}
            </div>

            {/* Recent-command history with live delivery status. */}
            <CommandHistory commands={data.commands} sentToken={sentToken} loading={data.loading} error={data.error} />
          </>
        )}
      </div>
    </WidgetFrame>
  );
}

// ── Parameter field ──────────────────────────────────────────────────────

function ParamField({
  widgetId,
  param,
  value,
  error,
  disabled,
  onChange,
}: {
  widgetId: string;
  param: CommandParameter;
  value: string;
  error?: string;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  // Scope the input id to the widget so two command-buttons sharing a parameter name
  // don't produce duplicate DOM ids (which would misdirect a label click to the first).
  const labelId = `cmd-${widgetId}-${param.name}`;
  const unit = param.unit ? ` (${param.unit})` : '';
  return (
    <label htmlFor={labelId} style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      <span style={{ fontSize: 12, color: css('muted-foreground') }}>
        {param.name}
        {unit}
        {param.required ? <span style={{ color: commandStatusColor('FAILED') }}> *</span> : null}
      </span>
      <ParamInput id={labelId} param={param} value={value} disabled={disabled} onChange={onChange} />
      {error ? (
        <span style={{ fontSize: 11, color: commandStatusColor('FAILED') }}>{error}</span>
      ) : null}
    </label>
  );
}

function ParamInput({
  id,
  param,
  value,
  disabled,
  onChange,
}: {
  id: string;
  param: CommandParameter;
  value: string;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  // An enum (of any data type) → a select; a boolean → a checkbox; a number → a numeric
  // input with the declared bounds; everything else → text.
  if (param.enum && param.enum.length > 0) {
    return (
      <select id={id} style={inputStyle} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
        {!param.required ? <option value="">—</option> : null}
        {param.enum.map((opt) => (
          <option key={opt} value={opt}>
            {opt}
          </option>
        ))}
      </select>
    );
  }
  if (param.dataType === 'BOOLEAN') {
    return (
      <input
        id={id}
        type="checkbox"
        style={{ width: 16, height: 16 }}
        checked={parseBool(value)}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked ? 'true' : 'false')}
      />
    );
  }
  const numeric = param.dataType === 'INT' || param.dataType === 'DOUBLE';
  return (
    <input
      id={id}
      type={numeric ? 'number' : 'text'}
      style={inputStyle}
      value={value}
      disabled={disabled}
      min={param.minValue ?? undefined}
      max={param.maxValue ?? undefined}
      step={param.dataType === 'INT' ? 1 : 'any'}
      placeholder={param.default ?? undefined}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

// ── History ────────────────────────────────────────────────────────────────

function CommandHistory({
  commands,
  sentToken,
  loading,
  error,
}: {
  commands: CommandStreamState['commands'];
  sentToken: string | null;
  loading: boolean;
  error: unknown;
}) {
  // An error is shown inline here (not by replacing the whole widget) so a transient poll
  // blip never tears down the still-usable send form. Last-good rows stay on screen when
  // present; only an empty-and-errored history shows the notice.
  const empty = commands.length === 0;
  return (
    <div
      style={{
        flex: 1,
        minHeight: 0,
        overflow: 'auto',
        borderTop: `1px solid ${css('border')}`,
        paddingTop: 6,
      }}
    >
      {empty ? (
        <div style={{ ...hint, textAlign: 'center', color: error ? commandStatusColor('FAILED') : css('muted-foreground') }}>
          {error ? 'Couldn’t load command history.' : loading ? 'Loading…' : 'No commands issued yet.'}
        </div>
      ) : (
        <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 4 }}>
          {commands.map((command) => (
            <li
              key={command.token}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                fontSize: 12,
                // Highlight the command just issued from this widget.
                fontWeight: command.token === sentToken ? 600 : 400,
              }}
            >
              <StatusBadge status={command.status} />
              <span style={{ color: css('foreground'), whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {command.name}
              </span>
              <span style={{ marginLeft: 'auto', color: css('muted-foreground'), whiteSpace: 'nowrap' }}>
                {formatDateTime(command.queuedTime)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  const color = commandStatusColor(status);
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 4,
        flexShrink: 0,
        fontSize: 11,
        fontWeight: 600,
        color,
      }}
    >
      <span aria-hidden="true" style={{ width: 7, height: 7, borderRadius: '50%', background: color }} />
      {commandStatusLabel(status)}
    </span>
  );
}

// ── styles / helpers ─────────────────────────────────────────────────────

const hint: CSSProperties = { fontSize: 11, color: css('muted-foreground') };

const inputStyle: CSSProperties = {
  width: '100%',
  boxSizing: 'border-box',
  padding: '4px 8px',
  fontSize: 13,
  borderRadius: 4,
  border: `1px solid ${css('border')}`,
  background: css('background'),
  color: css('foreground'),
};

const sendButton: CSSProperties = {
  padding: '6px 12px',
  fontSize: 13,
  fontWeight: 600,
  borderRadius: 4,
  border: 'none',
  background: css('primary'),
  color: css('background'),
  marginTop: 2,
};

function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
