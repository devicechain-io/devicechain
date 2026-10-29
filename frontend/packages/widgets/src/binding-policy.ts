// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The channel adapters' half of a widget's declarative binding policy: turn the descriptor's
// `binding` (typed constants and references to declared option keys) plus the widget's stored
// options into the subscription arguments a hub channel takes. This is the ONLY place that
// reads a policy-referenced option, so it reads them by the NAME the descriptor declares —
// no widget id is consulted, and an extension that declares `{ option: 'rows' }` is honored
// exactly as a built-in declaring `{ option: 'maxRows' }` is.

import {
  isValidSpanSeconds,
  type AlarmSubscription,
  type CommandSubscription,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';

import { optBoolean, optNumber, optString } from '@devicechain/widget-sdk';

// The shapes a policy value takes at runtime (the descriptor is validated at registration,
// so a ref names a declared option of a compatible kind; the value stored in a widget may
// still be absent or the wrong type, in which case the ref's default applies).
interface NumberBinding {
  const?: number;
  option?: string;
  default?: number;
  min?: number;
}
interface StringBinding {
  option?: string;
  default?: string;
}

function numberValue(binding: NumberBinding | undefined, options: WidgetInstance['options']): number | undefined {
  if (!binding) return undefined;
  let value: number | undefined;
  if (typeof binding.const === 'number') value = binding.const;
  else if (binding.option !== undefined) value = optNumber(options, binding.option) ?? binding.default;
  if (value === undefined) return undefined;
  return binding.min !== undefined ? Math.max(binding.min, value) : value;
}

function stringValue(binding: StringBinding | undefined, options: WidgetInstance['options']): string | undefined {
  if (!binding || binding.option === undefined) return undefined;
  return optString(options, binding.option) ?? binding.default;
}

function policy(descriptor: WidgetDescriptor): Record<string, unknown> {
  return descriptor.binding as Record<string, unknown>;
}

// The rolling-window size a measurement widget asked for, or undefined for the hook default.
export function measurementWindow(descriptor: WidgetDescriptor, widget: WidgetInstance): number | undefined {
  return numberValue(policy(descriptor).window as NumberBinding | undefined, widget.options);
}

// The span (seconds) a TIME-RANGED measurement widget draws, or undefined for one that is not.
// A widget is time-ranged by declaring `binding.windowSeconds`. Its option pins the span when set
// to a usable one; otherwise the widget follows the board's range (`boardSpan`, which a viewer
// can change). An option that is set but not a usable span (under a minute, over a week, not a
// whole number) is ignored rather than clamped, for the reason the parser gives for the board's
// own default: clamping turns a typo into a window nobody wrote.
export function measurementSpan(
  descriptor: WidgetDescriptor,
  widget: WidgetInstance,
  boardSpan: number | undefined,
): number | undefined {
  const binding = policy(descriptor).windowSeconds as NumberBinding | undefined;
  if (!binding) return undefined;
  const pinned = numberValue({ ...binding, min: undefined }, widget.options);
  return isValidSpanSeconds(pinned) ? pinned : boardSpan;
}

// Whether a measurement widget reads its datasource MEMBER BY MEMBER (a fleet widget: a row or a
// bar per device of an anchor) rather than as one stream keyed by measurement name. The descriptor
// says so with `binding.members`: always (`const`), or when a declared option holds a given value
// (a bar chart set to one bar per device).
export function membersMode(descriptor: WidgetDescriptor, widget: WidgetInstance): boolean {
  const members = policy(descriptor).members as { const?: boolean; option?: string; equals?: string } | undefined;
  if (!members) return false;
  if (members.const === true) return true;
  return members.option !== undefined && optString(widget.options, members.option) === members.equals;
}

// Whether a widget's `measurement` option is the one its MEMBERS mode compares, and so means nothing
// outside that mode: the descriptor gates members mode on an option (a chart set to one line or one
// bar per device) rather than always reading member by member. Read one device at a time, such a
// widget draws every measurement its datasource selects, so a `measurement` left stored from the
// members mode must not narrow anything (its freshness is the newest of what it draws).
export function measurementIsMembersOnly(descriptor: WidgetDescriptor): boolean {
  const members = policy(descriptor).members as { const?: boolean; option?: string } | undefined;
  return members !== undefined && members.const !== true && members.option !== undefined;
}

// Whether a measurement widget reads its devices' CONNECTIVITY (online or offline, last seen) rather
// than their readings. The descriptor says so with `binding.presence`. The feed that reads it is its
// own chunk (presence-feed.tsx), loaded only for such a widget.
export function presenceMode(descriptor: WidgetDescriptor): boolean {
  return (policy(descriptor).presence as { const?: boolean } | undefined)?.const === true;
}

// Whether a measurement widget reads one device's STORED HISTORY in time buckets rather than the live
// feed (`binding.history`). The feed that reads it is its own chunk (history-feed.tsx), loaded only for
// such a widget.
export function historyMode(descriptor: WidgetDescriptor): boolean {
  return (policy(descriptor).history as { const?: boolean } | undefined)?.const === true;
}

// The acknowledged filter is stored as 'true' / 'false' (or absent = any).
function parseAcknowledged(value: string | undefined): boolean | undefined {
  if (value === 'true') return true;
  if (value === 'false') return false;
  return undefined;
}

// An alarm widget's scope + filters. The datasource (undefined = tenant-wide) is the
// renderer's; the page size and filters come from the policy.
export function alarmSubscription(descriptor: WidgetDescriptor, widget: WidgetInstance): AlarmSubscription {
  const p = policy(descriptor);
  const deviceType = stringValue(p.deviceType as StringBinding | undefined, widget.options);
  return {
    datasource: widget.datasource,
    state: stringValue(p.state as StringBinding | undefined, widget.options),
    severity: stringValue(p.severity as StringBinding | undefined, widget.options),
    acknowledged: parseAcknowledged(stringValue(p.acknowledged as StringBinding | undefined, widget.options)),
    pageSize: numberValue(p.pageSize as NumberBinding | undefined, widget.options) ?? 1,
    // A widget that declares `binding.countBySeverity` reads counts, not rows; `binding.deviceType`
    // narrows them to one device type (a blank option is no type).
    ...((p.countBySeverity as { const?: boolean } | undefined)?.const === true ? { countBySeverity: true } : {}),
    ...(deviceType ? { deviceType } : {}),
  };
}

// Whether an alarm widget groups its alarms by the originating device's type (a tab strip). The
// descriptor says so with `binding.groupBy`: a declared option holding a given value turns it on.
export function groupsAlarmsByDeviceType(descriptor: WidgetDescriptor, widget: WidgetInstance): boolean {
  const groupBy = policy(descriptor).groupBy as { option?: string; equals?: string } | undefined;
  return groupBy?.option !== undefined && optString(widget.options, groupBy.option) === groupBy.equals;
}

// A command widget's target scope and how many recent commands its history shows.
export function commandSubscription(descriptor: WidgetDescriptor, widget: WidgetInstance): CommandSubscription {
  return {
    datasource: widget.datasource,
    pageSize: numberValue(policy(descriptor).pageSize as NumberBinding | undefined, widget.options) ?? 1,
  };
}

// Whether a measurement widget draws alarm or command markers: its descriptor declares the marker
// options and one of them is on. The feed that reads them is its own chunk (event-markers-feed.tsx),
// loaded only when this is true.
export function wantsEventMarkers(descriptor: WidgetDescriptor, widget: WidgetInstance): boolean {
  const declared = descriptor.options as Record<string, unknown>;
  return (
    (declared.markAlarms !== undefined && optBoolean(widget.options, 'markAlarms') === true) ||
    (declared.markCommands !== undefined && optBoolean(widget.options, 'markCommands') === true)
  );
}

// Whether a widget takes the stored history as a seed. Every measurement widget does. An aggregate
// widget reads its own aggregates and takes none, except a card showing the LATEST reading: the live
// stream only delivers what arrives after the board opens, so a device that reports every few
// minutes would leave it saying there is no data while its readings are stored.
export function takesHistorySeed(descriptor: WidgetDescriptor, widget: WidgetInstance): boolean {
  return descriptor.channel === 'measurement' || (descriptor.channel === 'aggregate' && optString(widget.options, 'stat') === 'last');
}
