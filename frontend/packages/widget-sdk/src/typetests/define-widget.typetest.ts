// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Type-level tests for defineWidget. Compiled by the package's ordinary `tsc --noEmit` (and
// so by `npm run typecheck`); vitest never runs it (the name does not match *.test.ts) and
// the build excludes it.
//
// Every NEGATIVE case is a `// @ts-expect-error` placed directly above the offending
// property. The directive is itself the assertion: if the line ever starts compiling, tsc
// reports "Unused '@ts-expect-error' directive" and the typecheck fails. To see a case can
// fail, delete its directive line and `npm run typecheck` goes red on that property.

import type { AlarmStreamState, MeasurementStreamState } from '@devicechain/dashboards';

import { defineWidget } from '../define-widget';
import type { WidgetProps } from '../widget';

type Equals<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;
type Expect<T extends true> = T;

const OPTIONS = {
  title: { kind: 'string', doc: 'Heading.' },
  mode: { kind: 'enum', values: ['fast', 'slow'], doc: 'Speed.' },
  window: { kind: 'number', integer: true, min: 1, doc: 'Window.' },
} as const;

const meta = {
  version: '1.0.0',
  sdkApiVersion: 1,
  optionsVersion: 1,
  display: { title: 'T' },
} as const;

const measurementComponent = (_props: WidgetProps<MeasurementStreamState>) => null;
const alarmComponent = (_props: WidgetProps<AlarmStreamState>) => null;

// ---- Positive ---------------------------------------------------------------

export const ok = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/ok',
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    binding: { window: { option: 'window' } },
    options: OPTIONS,
    defaults: { mode: 'fast' },
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: measurementComponent },
});

// Inference: the channel stays a literal, and a component written inline receives the
// channel's snapshot as `data`.
export type OkChannel = Expect<Equals<typeof ok.descriptor.channel, 'measurement'>>;

export const inferred = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/inferred',
    channel: 'alarm',
    datasource: { mode: 'optional', selectors: ['device'] },
    binding: { pageSize: { const: 50 }, state: { option: 'mode' } },
    options: OPTIONS,
    defaults: {},
    interactions: { alarmActions: true },
  },
  implementation: {
    kind: 'trusted-react',
    component: (props) => {
      const data = props.data;
      type DataIsAlarmSnapshot = Expect<Equals<typeof data, AlarmStreamState>>;
      void (null as unknown as DataIsAlarmSnapshot);
      return null;
    },
  },
});

export const selection = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/selection',
    channel: 'selection',
    datasource: { mode: 'none' },
    binding: {},
    options: OPTIONS,
    defaults: {},
    interactions: { selection: true, candidates: true },
  },
  implementation: {
    kind: 'trusted-react',
    component: (props) => {
      const data = props.data;
      type SelectionDataIsNull = Expect<Equals<typeof data, null>>;
      void (null as unknown as SelectionDataIsNull);
      return null;
    },
  },
});

// ---- Negative ---------------------------------------------------------------

export const alarmComponentOnMeasurement = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    binding: {},
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  // @ts-expect-error a component that renders alarm data cannot sit on the measurement channel
  implementation: { kind: 'trusted-react', component: alarmComponent },
});

export const wrongDefaultType = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    binding: {},
    options: OPTIONS,
    // @ts-expect-error `window` is a number option
    defaults: { window: 'many' },
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: measurementComponent },
});

export const enumDefaultOutsideValues = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    binding: {},
    options: OPTIONS,
    // @ts-expect-error 'medium' is not one of the declared enum values
    defaults: { mode: 'medium' },
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: measurementComponent },
});

export const windowRefToStringOption = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    // @ts-expect-error `title` is a string option, not a number option
    binding: { window: { option: 'title' } },
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: measurementComponent },
});

export const windowRefToUndeclaredKey = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    // @ts-expect-error `ghost` is not a declared option key
    binding: { window: { option: 'ghost' } },
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: measurementComponent },
});

export const unsupportedApiVersion = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    // @ts-expect-error only sdkApiVersion 1 exists
    sdkApiVersion: 2,
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    binding: {},
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: measurementComponent },
});

export const commandSendOnMeasurement = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    binding: {},
    options: OPTIONS,
    defaults: {},
    // @ts-expect-error commandSend belongs to the control channel
    interactions: { commandSend: true },
  },
  implementation: { kind: 'trusted-react', component: measurementComponent },
});

export const functionInDescriptor = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    // @ts-expect-error a descriptor carries data only, never a function
    display: { title: 'Bad', render: () => null },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    binding: {},
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: measurementComponent },
});

export const requiredDatasourceOnNone = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    channel: 'none',
    // @ts-expect-error a `none` channel opens no data channel, so it cannot require a datasource
    datasource: { mode: 'required', selectors: ['device'] },
    binding: {},
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: (_props: WidgetProps<null>) => null },
});

export const wrongImplementationKind = defineWidget({
  descriptor: {
    ...meta,
    id: 'acme/bad',
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device'] },
    binding: {},
    options: OPTIONS,
    defaults: {},
    interactions: {},
  },
  // @ts-expect-error the only execution kind in this release is 'trusted-react'
  implementation: { kind: 'sandboxed-iframe', component: measurementComponent },
});
