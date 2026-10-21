// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Type-level tests for the portable widget descriptor. Compiled by the package's ordinary
// `tsc --noEmit` (and so by `npm run typecheck`); vitest never runs it (the name does not
// match *.test.ts) and the build excludes it.
//
// Every NEGATIVE case is a `// @ts-expect-error` placed directly above the offending
// property. The directive is itself the assertion: if the line ever starts compiling, tsc
// reports "Unused '@ts-expect-error' directive" and the typecheck fails. To see a case can
// fail, delete its directive line and `npm run typecheck` goes red on that property.

import { defineWidgetDescriptor } from '../widget-descriptor';
import type { OptionsOf, WidgetOptionSpecs } from '../option-schema';
import type { WidgetLayoutSpec } from '../widget-descriptor';

type Equals<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;
type Expect<T extends true> = T;

const OPTIONS = {
  title: { kind: 'string', doc: 'Heading.' },
  required: { kind: 'string', required: true, doc: 'Needed.' },
  mode: { kind: 'enum', values: ['fast', 'slow'], doc: 'Speed.' },
  window: { kind: 'number', integer: true, min: 1, doc: 'Window.' },
  size: { kind: 'number', doc: 'Size.' },
  flag: { kind: 'boolean', doc: 'Flag.' },
} as const satisfies WidgetOptionSpecs;

// ---- Positive ---------------------------------------------------------------

export const ok = defineWidgetDescriptor({
  id: 'acme/ok',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Ok' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  binding: { window: { option: 'window', min: 1 } },
  options: OPTIONS,
  constraints: [{ kind: 'greater-than', key: 'size', than: 'window' }],
  // `required` does not bind defaults, and an enum default is checked against its values.
  defaults: { title: 'x', mode: 'fast', size: 3 },
  interactions: { selection: true },
});

// The channel is inferred as a literal, not widened.
export type OkChannel = Expect<Equals<typeof ok.channel, 'measurement'>>;

// OptionsOf infers enum literals and keeps required keys mandatory.
export type Bag = OptionsOf<typeof OPTIONS>;
export type EnumLiteral = Expect<Equals<Bag['mode'], 'fast' | 'slow' | undefined>>;
export type RequiredIsMandatory = Expect<Equals<Bag['required'], string>>;
export type NumberIsNumber = Expect<Equals<Bag['size'], number | undefined>>;

// ---- Negative ---------------------------------------------------------------

export const wrongDefaultType = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  binding: {},
  options: OPTIONS,
  // @ts-expect-error a string default for a number option
  defaults: { size: 'big' },
  interactions: {},
});

export const enumOutsideValues = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  binding: {},
  options: OPTIONS,
  // @ts-expect-error 'medium' is not one of the declared enum values
  defaults: { mode: 'medium' },
  interactions: {},
});

export const windowRefToStringOption = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  // @ts-expect-error `title` is a string option, not a number option
  binding: { window: { option: 'title' } },
  options: OPTIONS,
  defaults: {},
  interactions: {},
});

export const windowRefToUndeclaredKey = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  // @ts-expect-error `ghost` is not a declared option key
  binding: { window: { option: 'ghost' } },
  options: OPTIONS,
  defaults: {},
  interactions: {},
});

export const unsupportedApiVersion = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  // @ts-expect-error only sdkApiVersion 1 exists
  sdkApiVersion: 2,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  binding: {},
  options: OPTIONS,
  defaults: {},
  interactions: {},
});

export const commandSendOnMeasurement = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  binding: {},
  options: OPTIONS,
  defaults: {},
  // @ts-expect-error commandSend belongs to the control channel
  interactions: { commandSend: true },
});

export const functionInDescriptor = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  // @ts-expect-error a descriptor carries data only, never a function
  display: { title: 'Bad', render: () => null },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  binding: {},
  options: OPTIONS,
  defaults: {},
  interactions: {},
});

export const requiredDatasourceOnNone = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'none',
  // @ts-expect-error a `none` channel opens no data channel, so it cannot require a datasource
  datasource: { mode: 'required', selectors: ['device'] },
  binding: {},
  options: OPTIONS,
  defaults: {},
  interactions: {},
});

export const anchorOnControl = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'control',
  // @ts-expect-error a command target must be a device, so control offers device and slot only
  datasource: { mode: 'required', selectors: ['anchor'] },
  binding: { pageSize: { const: 20 } },
  options: OPTIONS,
  defaults: {},
  interactions: {},
});

export const constraintOnNonNumberKey = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'Bad' },
  channel: 'measurement',
  datasource: { mode: 'required', selectors: ['device'] },
  binding: {},
  options: OPTIONS,
  // @ts-expect-error `title` is a string option; a greater-than constraint needs numbers
  constraints: [{ kind: 'greater-than', key: 'title', than: 'size' }],
  defaults: {},
  interactions: {},
});

// ---- Layout -----------------------------------------------------------------

export const sized = defineWidgetDescriptor({
  id: 'acme/sized',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'T' },
  channel: 'none',
  datasource: { mode: 'none' },
  binding: {},
  options: OPTIONS,
  defaults: {},
  interactions: {},
  layout: { defaultSpan: { cols: 8, rows: 2 }, minSpan: { cols: 4, rows: 2 }, resize: 'horizontal' },
});

// `layout` stays optional: the examples above compile without it.
export type LayoutIsOptional = Expect<Equals<typeof ok.layout, WidgetLayoutSpec | undefined>>;

export const badResizeMode = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'T' },
  channel: 'none',
  datasource: { mode: 'none' },
  binding: {},
  options: OPTIONS,
  defaults: {},
  interactions: {},
  // @ts-expect-error resize is one of both, horizontal, vertical or none
  layout: { defaultSpan: { cols: 8, rows: 4 }, resize: 'diagonal' },
});

export const missingDefaultSpan = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'T' },
  channel: 'none',
  datasource: { mode: 'none' },
  binding: {},
  options: OPTIONS,
  defaults: {},
  interactions: {},
  // @ts-expect-error a layout block must say what size a new widget takes
  layout: { minSpan: { cols: 2, rows: 1 } },
});

export const stringSpan = defineWidgetDescriptor({
  id: 'acme/bad',
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'T' },
  channel: 'none',
  datasource: { mode: 'none' },
  binding: {},
  options: OPTIONS,
  defaults: {},
  interactions: {},
  // @ts-expect-error spans are counted in cells, not CSS strings
  layout: { defaultSpan: { cols: '8', rows: 4 } },
});

// ---- An option's doc is REQUIRED on the public type ---------------------------
// The built-ins' render-only descriptors omit it (typed in @devicechain/widgets, not here); every
// descriptor a package publishes carries it, so a reader gets `string`, never `string | undefined`,
// and an extension author is told at compile time, not first at registration.
// @ts-expect-error -- an option spec with no doc
export const undocumentedSpec: WidgetOptionSpecs = { title: { kind: 'string' } };
export type DocIsAString = Expect<Equals<WidgetOptionSpecs[string]['doc'], string>>;
