// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The widget contract — the props every widget component receives.

import type {
  MeasurementStreamState,
  SlotDefinition,
  WidgetActions,
  WidgetDescriptor,
  WidgetInstance,
} from '@devicechain/dashboards';
import type { ComponentType } from 'react';

// A widget is a PURE React component of its definition instance and already-
// resolved live data — it never touches the DashboardHub directly. This is the
// public custom-widget interface: a widget is a function of (widget, data), so it
// renders identically from a live stream, a recorded window, or a test fixture.
// The host binds the hub to this contract and dispatches on the widget's data
// CHANNEL: measurement widgets receive a MeasurementStreamState (the default), alarm
// widgets an AlarmStreamState, and so on. The data type is a generic so each widget
// declares exactly the shape it consumes.
export interface WidgetProps<D = MeasurementStreamState> {
  widget: WidgetInstance;
  data: D;
  // The action seam (writes), threaded in by the renderer. Undefined when the runtime
  // supplies none — an acting widget then renders read-only. Present for the live hub and
  // the synthetic preview source alike, so an action widget renders identically in both.
  actions?: WidgetActions;
}

export type WidgetComponent<D = MeasurementStreamState> = ComponentType<WidgetProps<D>>;

// One command a device accepts, as a command picker needs it: the key the enqueue gate
// matches, the label to show, and the parameter descriptors a button bakes (JSON text).
export interface EditorCommandChoice {
  commandKey: string;
  name?: string | null;
  parameterSchema?: string | null;
}

// What a host can tell an editor about a device's commands: the ones the enqueue gate will
// accept (`selectable`), the authored-but-unpublished keys it withholds (`draftOnly`, named so
// an author who just wrote one learns it is unpublished), and whether the profile restricts
// the vocabulary at all.
export interface EditorCommandChoices {
  selectable: EditorCommandChoice[];
  draftOnly: string[];
  constrained: boolean;
}

// The host services an editor may use, passed EXPLICITLY. An editor imports nothing from the
// host application: whatever it needs beyond its own options arrives here, and a service the
// host cannot supply is simply absent (an editor must cope, as a widget copes without actions).
export interface WidgetEditorServices {
  // The dashboard's slots, keyed by name — the candidate targets for a slot-picking control.
  // Empty on a slot-free dashboard.
  readonly slots: Readonly<Record<string, SlotDefinition>>;
  // The device token of the widget's datasource, when it is bound to one.
  readonly deviceToken: string | undefined;
  // The commands the given device accepts. Absent when the host has no command vocabulary.
  readonly commandChoices?: (deviceToken: string) => Promise<EditorCommandChoices>;
}

// What a registered editor receives. `options` is the widget's stored settings. The ONLY way
// to change them is `setOptions`, which the host validates against the descriptor's schema
// before applying: a key the descriptor does not declare, or a value of the wrong kind, throws
// at the moment the editor makes the mistake rather than surfacing as a broken widget at
// publish. `undefined`, an empty string and `false` clear a key; a number is folded into its
// declared bounds. Several keys in one call apply together (one undo step, one render).
export interface WidgetEditorProps<O = Record<string, unknown>> {
  readonly options: Readonly<Partial<O>>;
  readonly setOptions: (patch: { [K in keyof O]?: O[K] | undefined }) => void;
  readonly services: WidgetEditorServices;
  // The registration's own descriptor: its option specs (for labels, bounds, enum values)
  // and its display text.
  readonly descriptor: WidgetDescriptor;
}
