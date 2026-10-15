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

import { optNumber, optString } from '@devicechain/widget-sdk';

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
  return {
    datasource: widget.datasource,
    state: stringValue(p.state as StringBinding | undefined, widget.options),
    severity: stringValue(p.severity as StringBinding | undefined, widget.options),
    acknowledged: parseAcknowledged(stringValue(p.acknowledged as StringBinding | undefined, widget.options)),
    pageSize: numberValue(p.pageSize as NumberBinding | undefined, widget.options) ?? 1,
  };
}

// A command widget's target scope and how many recent commands its history shows.
export function commandSubscription(descriptor: WidgetDescriptor, widget: WidgetInstance): CommandSubscription {
  return {
    datasource: widget.datasource,
    pageSize: numberValue(policy(descriptor).pageSize as NumberBinding | undefined, widget.options) ?? 1,
  };
}
