// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// WIDGET_OPTIONS — the typed schema for each widget type's `options` bag, as a by-id view.
//
// A WidgetInstance's `options` is `Record<string, unknown>` in the stored definition:
// dashboard-management keeps a definition as opaque JSON, and the definition parser does
// not look inside `options` at all. Each built-in's descriptor (builtin-descriptors.ts)
// declares every option the RENDERER actually reads, with its type and legal values; this
// module projects those schemas by id and wraps the shared engine under the by-type names
// the console and fixtures use:
//   • validateWidgetOptions() — the runtime check, for anything that PRODUCES a definition
//     (a generated dashboard must not ship an options blob the renderer ignores).
//   • WidgetOptions<T>        — the compile-time shape, for code that builds options.
//
// The schema describes the RENDERER, because that is the contract deciding what a viewer
// actually sees. Where the console's authoring UI offers fewer keys than this — it does:
// `align`, `fontSize`, `fit`, `window` and `measurement` are all honored by the renderer
// and not authorable in the config panel — the gap is in the panel, not here.
//
// The spec VOCABULARY and the engine that checks a bag against a schema live in
// @devicechain/dashboards (option-schema.ts) — they know no widget type, so the SDK's
// registry validates an extension's schema with the very code that validates this one.

import {
  clampNumberOption,
  enumValues,
  numberSpec,
  validateOptions,
  type NumberOptionSpec,
  type OptionConstraint,
  type OptionIssueCode,
  type BuiltinWidgetId,
  type OptionsOf,
  type WidgetOptionSpecs,
  type WidgetType,
} from '@devicechain/dashboards';

import { builtinWidgetCatalog, builtinWidgetDescriptors } from './builtin-descriptors';

export type {
  BooleanOptionSpec,
  EnumOptionSpec,
  NumberOptionSpec,
  OptionIssueCode,
  OptionSpec,
  StringOptionSpec,
  WidgetOptionSpecs,
} from '@devicechain/dashboards';
export { clampNumberOption };

// ---- The derived by-id view -------------------------------------------------

// Every built-in's option schema, keyed by id. A frozen PROJECTION of the descriptors in
// builtin-descriptors.ts (each schema is declared there, once, beside the widget's channel and
// binding policy); it holds no schema of its own, so it cannot drift from what the registry
// validates. The cross-field rules (gauge's max > min) travel on the descriptor as data.
type OptionsById = { readonly [K in BuiltinWidgetId]: (typeof builtinWidgetDescriptors)[K]['options'] };

export const WIDGET_OPTIONS: OptionsById = Object.freeze(
  Object.fromEntries(Object.entries(builtinWidgetDescriptors).map(([id, d]) => [id, d.options])),
) as OptionsById;

function constraintsFor(widgetType: WidgetType): readonly OptionConstraint[] {
  return builtinWidgetCatalog.get(widgetType)?.constraints ?? [];
}

// ---- Derived compile-time types ---------------------------------------------

// WidgetOptions<'gauge'> is the typed options bag for a gauge: every declared key at its
// declared type, required ones mandatory. Code that BUILDS a definition should type its
// options with this — a typo is then a compile error rather than a key nothing reads.
//
// DISTRIBUTIVE over T, which is the whole reason it is written as a conditional. `keyof` a
// union of the built-in tables is their INTERSECTION — only `title` — and the required-keys
// computation over that union collapses to never, so the undistributed form silently
// degenerates for any generic T: it would reject `{ text: 'body' }` as an excess property
// while accepting `{}` for a widget that requires an option. That is exactly the position a
// generator writing one widget of each type sits in. Distributing makes
// WidgetOptions<WidgetType> the union of the per-type bags instead.
export type WidgetOptions<T extends WidgetType> = T extends WidgetType ? OptionsOf<(typeof WIDGET_OPTIONS)[T]> : never;

// ---- Runtime validation -----------------------------------------------------

// The by-type issue shape the console reads. `widgetType` keeps its name so existing
// consumers compile unchanged; the engine's own issue calls the same value `widgetId`.
export interface OptionIssue {
  widgetType: WidgetType;
  // The offending option key. For a cross-field rule, the key the fix belongs on.
  key: string;
  code: OptionIssueCode;
  message: string;
}

// validateWidgetOptions reports every way a widget's stored options bag disagrees with
// what the renderer reads. An empty result means every key present is one the renderer
// honors, at a value it accepts, and no required option is missing.
//
// It is deliberately a REPORT rather than a throw: the renderer is tolerant by design and
// must stay that way for a hand-edited definition, so the strictness belongs to whoever
// produces definitions (fixtures, generators, the config panel) and to the tests that
// gate them. Callers decide which codes are fatal — a stored dashboard carrying an
// 'unknown' leftover is untidy; a generated one carrying 'missing' is broken.
export function validateWidgetOptions(
  widgetType: WidgetType,
  options: Record<string, unknown> | undefined,
): OptionIssue[] {
  const specs: WidgetOptionSpecs = WIDGET_OPTIONS[widgetType];
  return validateOptions(specs, options, { constraints: constraintsFor(widgetType), label: widgetType }).map(
    ({ key, code, message }) => ({ widgetType, key, code, message }),
  );
}

// numberOptionSpec returns the declared bounds for a numeric option, so an authoring
// UI can constrain its input from the schema rather than from a second, hand-written
// copy of the same numbers. See numberSpec for why it exists.
export function numberOptionSpec(type: WidgetType, key: string): NumberOptionSpec | undefined {
  return numberSpec(WIDGET_OPTIONS[type], key);
}

// enumOptionValues returns the legal values of an enum option, so an authoring UI can
// build its dropdown from the schema instead of a private copy of the same list.
export function enumOptionValues(type: WidgetType, key: string): readonly string[] | undefined {
  return enumValues(WIDGET_OPTIONS[type], key);
}
