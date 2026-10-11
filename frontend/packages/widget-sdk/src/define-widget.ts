// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type {
  ChannelSnapshot,
  OptionsOf,
  WidgetChannel,
  WidgetDescriptor,
  WidgetOptionSpecs,
} from '@devicechain/dashboards';
import type { ComponentType } from 'react';

import type { WidgetEditorProps, WidgetProps } from './widget';

// The trusted half of a registration: the React code. Kept apart from the descriptor so
// the descriptor stays portable plain data and headless validators never load a component.
export interface WidgetImplementation<C extends WidgetChannel, S extends WidgetOptionSpecs> {
  // The only execution kind in this release: React code the host bundled at build time,
  // running with the host page's privileges.
  kind: 'trusted-react';
  component: ComponentType<WidgetProps<ChannelSnapshot<C>>>;
  editor?: ComponentType<WidgetEditorProps<OptionsOf<S>>>;
}

export interface WidgetDefinition<
  C extends WidgetChannel = WidgetChannel,
  S extends WidgetOptionSpecs = WidgetOptionSpecs,
> {
  descriptor: WidgetDescriptor<C, S>;
  implementation: WidgetImplementation<C, S>;
}

// A component with its props type erased. `any` is deliberate and confined to this alias:
// `ComponentType<never>` would be the principled erasure, but a class component's
// `defaultProps` is covariant in its props and rejects it, and a registry has to hold
// components of every channel's props.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type ErasedComponent = ComponentType<any>;

// A definition with its component types erased, which is what a registry stores: any
// specific WidgetDefinition<C, S> is assignable to it.
export interface ErasedWidgetDefinition {
  descriptor: WidgetDescriptor;
  implementation: {
    kind: 'trusted-react';
    component: ErasedComponent;
    editor?: ErasedComponent;
  };
}

// defineWidget is an identity at runtime; it exists for inference. The channel and the
// option schema are inferred from the literal, so a component whose data disagrees with
// the channel, defaults that disagree with the schema, a binding that names a missing or
// wrong-kind option, or an interaction the channel cannot carry is a COMPILE error — and
// createWidgetRegistry checks the same things again at runtime for callers that bypass the
// compiler. Every widget, built-in or third-party, is declared through this one helper.
export function defineWidget<const C extends WidgetChannel, const S extends WidgetOptionSpecs>(
  definition: WidgetDefinition<C, S>,
): WidgetDefinition<C, S> {
  return definition;
}
