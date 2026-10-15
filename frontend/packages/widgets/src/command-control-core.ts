// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the command slider and toggle: which parameter a control is bound to and why
// it may refuse to be, the payload it sends, and the last value sent read back off the command
// history. Nothing here reads a widget's options (the widgets hand the values in), so it is
// tested on its own.
//
// 🔴 A CONTROL THAT CANNOT SEND SAYS SO. Every way the binding can be wrong is a named refusal
// with the parameter it concerns, never a fallback value: a control that guesses at its range, its
// type or a required parameter sends a plausible command the author did not mean.

import type { CommandParameter, CommandRow } from '@devicechain/dashboards';

import { buildPayload, isScalar, parseParameterSchema } from './widgets/command-params';

export type ControlKind = 'number' | 'boolean';

export type Refusal =
  | { code: 'noCommand' }
  | { code: 'setParameter' }
  | { code: 'parameterMissing'; name: string }
  | { code: 'wrongType'; name: string; kind: ControlKind }
  | { code: 'fixedChoices'; name: string }
  | { code: 'otherRequired'; names: string[] }
  | { code: 'setRange' }
  | { code: 'badRange' };

export interface BindInput {
  commandName: string | undefined;
  parameterSchema: string | null | undefined;
  parameter: string | undefined;
  // Authored fallbacks for a slider: used only where the command definition leaves the end (or
  // the step) open. The definition is the authority on what the command accepts.
  min?: number;
  max?: number;
  step?: number;
}

export interface Range {
  min: number;
  max: number;
  step: number;
  integer: boolean;
}

export type Binding =
  | { ok: true; kind: ControlKind; param: CommandParameter; range?: Range; sent: CommandParameter[] }
  | { ok: false; refusal: Refusal };

const refuse = (refusal: Refusal): Binding => ({ ok: false, refusal });

// 100 notches across a continuous range: the step a DOUBLE slider gets when nobody set one.
const DEFAULT_NOTCHES = 100;

export function bindControl(kind: ControlKind, input: BindInput): Binding {
  if (!input.commandName) return refuse({ code: 'noCommand' });
  const params = parseParameterSchema(input.parameterSchema);
  const wanted = (p: CommandParameter) =>
    isScalar(p) && (kind === 'boolean' ? p.dataType === 'BOOLEAN' : p.dataType === 'INT' || p.dataType === 'DOUBLE');

  let param: CommandParameter | undefined;
  if (input.parameter) {
    param = params.find((p) => p.name === input.parameter);
    if (!param) return refuse({ code: 'parameterMissing', name: input.parameter });
    if (!wanted(param)) return refuse({ code: 'wrongType', name: param.name, kind });
  } else {
    // Unambiguous or not at all: one candidate of the right type is the parameter; none or several
    // is the author's to resolve.
    const candidates = params.filter(wanted);
    if (candidates.length !== 1) return refuse({ code: 'setParameter' });
    param = candidates[0];
  }
  if (param.enum && param.enum.length > 0) return refuse({ code: 'fixedChoices', name: param.name });

  // Every other parameter either has a declared default (sent as is) or is optional (omitted).
  // A required one with neither would have to be invented, so the control refuses instead.
  const others = params.filter((p) => p !== param);
  const unsatisfied = others.filter((p) => p.required && (!isScalar(p) || p.default == null || p.default === ''));
  if (unsatisfied.length > 0) return refuse({ code: 'otherRequired', names: unsatisfied.map((p) => p.name) });
  const sent = [param, ...others.filter((p) => isScalar(p) && p.default != null && p.default !== '')];

  if (kind === 'boolean') return { ok: true, kind, param, sent };

  const min = param.minValue ?? input.min;
  const max = param.maxValue ?? input.max;
  if (min === undefined || max === undefined) return refuse({ code: 'setRange' });
  if (!(max > min)) return refuse({ code: 'badRange' });
  const integer = param.dataType === 'INT';
  const step = input.step !== undefined && input.step > 0 ? input.step : integer ? 1 : (max - min) / DEFAULT_NOTCHES;
  return { ok: true, kind, param, range: { min, max, step, integer }, sent };
}

// The decimals a step carries, so snapped values do not pick up float noise (0.1 steps).
function decimalsOf(n: number): number {
  const text = String(n);
  const e = /e-(\d+)$/.exec(text);
  if (e) return Number(e[1]);
  const dot = text.indexOf('.');
  return dot < 0 ? 0 : text.length - dot - 1;
}

// clampToRange puts a raw value on the slider's grid: inside [min, max], on a whole step from min
// (an INT rounds to a whole number whatever the step), and never NaN.
export function clampToRange(range: Range, raw: number): number {
  const finite = Number.isFinite(raw) ? raw : range.min;
  const bounded = Math.min(range.max, Math.max(range.min, finite));
  const steps = Math.round((bounded - range.min) / range.step);
  const decimals = Math.max(decimalsOf(range.step), decimalsOf(range.min));
  let snapped = Number((range.min + steps * range.step).toFixed(Math.min(decimals, 12)));
  if (snapped > range.max) snapped = Number((snapped - range.step).toFixed(Math.min(decimals, 12)));
  if (range.integer) snapped = Math.round(snapped);
  return Math.min(range.max, Math.max(range.min, snapped));
}

// The JSON the command carries: the bound parameter as its real type, the other parameters that
// have a declared default, nothing else.
export function payloadFor(binding: Extract<Binding, { ok: true }>, value: number | boolean): string {
  const values: Record<string, string> = {};
  for (const p of binding.sent) {
    values[p.name] = p.default ?? '';
    if (p === binding.param) values[p.name] = String(value);
  }
  const payload = buildPayload(binding.sent, values);
  // sent always holds the bound parameter, so a payload always exists.
  return payload as string;
}

export type DeliveryGroup = 'pending' | 'delivered' | 'failed' | 'unknown';

// Which of the three things an operator asks of a command its status answers. An unrecognised
// status is its own group, rendered as the raw word: it is not known to be pending.
export function deliveryGroup(status: string): DeliveryGroup {
  switch (status) {
    case 'QUEUED':
    case 'HELD':
    case 'SENT':
    case 'PARKED':
      return 'pending';
    case 'SUCCESSFUL':
      return 'delivered';
    case 'FAILED':
    case 'TIMEOUT':
    case 'EXPIRED':
    case 'CANCELLED':
      return 'failed';
    default:
      return 'unknown';
  }
}

export interface LastSent {
  token: string;
  // The value the command carried for the bound parameter; undefined when the command (sent from
  // somewhere else) did not carry one this control can read.
  value: number | boolean | undefined;
  status: string;
}

// An accepted send not yet seen in the history.
export interface Optimistic {
  token: string;
  value: number | boolean;
}

function readValue(kind: ControlKind, payload: string | null, name: string): number | boolean | undefined {
  if (!payload) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(payload);
  } catch {
    return undefined;
  }
  if (typeof parsed !== 'object' || parsed === null) return undefined;
  const v = (parsed as Record<string, unknown>)[name];
  if (kind === 'number') return typeof v === 'number' && Number.isFinite(v) ? v : undefined;
  if (typeof v === 'boolean') return v;
  return undefined;
}

// lastSent reads the newest command of this control's name off the history. It is the newest, not
// the newest that happens to parse: skipping to an older row would report a value that has since
// been superseded. A send the history has not caught up with yet stands in until it does.
export function lastSent(
  kind: ControlKind,
  commands: readonly CommandRow[],
  commandName: string,
  parameter: string,
  optimistic: Optimistic | null,
): LastSent | null {
  let newest: CommandRow | undefined;
  for (const c of commands) {
    if (c.name !== commandName) continue;
    if (!newest || (c.queuedTime ?? '') > (newest.queuedTime ?? '')) newest = c;
  }
  if (optimistic && !commands.some((c) => c.token === optimistic.token)) {
    return { token: optimistic.token, value: optimistic.value, status: 'QUEUED' };
  }
  if (!newest) return null;
  return { token: newest.token, value: readValue(kind, newest.payload, parameter), status: newest.status };
}
