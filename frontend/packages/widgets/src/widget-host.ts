// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the host hands one widget: ONLY the interactions its descriptor declares,
// intersected with what the host supports. Declaring an interaction is a request, and this
// narrowing is API hygiene, not a security boundary (trusted React runs with the page's
// privileges); the server stays the authority on every write.

import type { WidgetActions, WidgetDescriptor } from '@devicechain/dashboards';

import { type WidgetCandidates, type WidgetSelect } from '@devicechain/widget-sdk';

// Receives the message of an async host callback that rejected, so it can be shown on the
// widget that made the call.
export type ReportRejection = (message: string) => void;

export function describeError(error: unknown): string {
  if (error instanceof Error) return error.message || error.name;
  return String(error);
}

// guardAsync reports a rejection to the widget's local diagnostics and rethrows it, so a
// widget that handles the failure itself (a refusal it shows inline) still sees it.
function guardAsync<A extends unknown[], R>(
  fn: (...args: A) => Promise<R>,
  report: ReportRejection,
): (...args: A) => Promise<R> {
  return (...args: A) => {
    let pending: Promise<R>;
    try {
      pending = Promise.resolve(fn(...args));
    } catch (e) {
      report(describeError(e));
      return Promise.reject(e);
    }
    return pending.catch((e: unknown) => {
      report(describeError(e));
      throw e;
    });
  };
}

const ALARM_AUTHORITY = 'alarm:write';
const COMMAND_AUTHORITY = 'command:write';

// A call to an interaction the widget did not declare rejects, and the rejection is reported
// on the widget like any other failed host callback: a widget that fires and forgets
// (`void actions.acknowledgeAlarm(t)`) must still SHOW the refusal, not swallow it.
function undeclared(capability: string, report: ReportRejection): () => Promise<never> {
  return guardAsync(
    () => Promise.reject(new Error(`this widget did not declare the ${capability} interaction`)),
    report,
  );
}

// narrowActions returns the action seam a widget may use. A widget that declares neither
// alarm actions nor command send gets none; one that declares a subset gets the declared
// methods working and the rest refusing loudly (never a silent no-op), and `can` denies the
// write authorities of capabilities it did not declare.
export function narrowActions(
  actions: WidgetActions | undefined,
  descriptor: WidgetDescriptor,
  report: ReportRejection,
): WidgetActions | undefined {
  if (!actions) return undefined;
  const alarm = descriptor.interactions.alarmActions === true;
  const command = descriptor.interactions.commandSend === true;
  if (!alarm && !command) return undefined;
  const narrowed: WidgetActions = {
    acknowledgeAlarm: alarm
      ? guardAsync((t) => actions.acknowledgeAlarm(t), report)
      : undeclared('alarmActions', report),
    clearAlarm: alarm ? guardAsync((t) => actions.clearAlarm(t), report) : undeclared('alarmActions', report),
    can: (authority) => {
      if (authority === ALARM_AUTHORITY && !alarm) return false;
      if (authority === COMMAND_AUTHORITY && !command) return false;
      return actions.can(authority);
    },
  };
  // Left absent when undeclared or unsupported: a command widget feature-detects it and
  // renders its Send control disabled.
  if (command && typeof actions.sendCommand === 'function') {
    narrowed.sendCommand = guardAsync((d, n, p) => actions.sendCommand!(d, n, p), report);
  }
  return narrowed;
}

// narrowSelect: the view-selection callback, only for a widget that declared `selection`.
export function narrowSelect(select: WidgetSelect | undefined, descriptor: WidgetDescriptor): WidgetSelect | undefined {
  return descriptor.interactions.selection === true ? select : undefined;
}

// narrowCandidates: the candidate lookup, only for a widget that declared `candidates`; its
// rejections are reported locally and rethrown.
export function narrowCandidates(
  candidates: WidgetCandidates | undefined,
  descriptor: WidgetDescriptor,
  report: ReportRejection,
): WidgetCandidates | undefined {
  if (!candidates || descriptor.interactions.candidates !== true) return undefined;
  return guardAsync((slot) => candidates(slot), report);
}
