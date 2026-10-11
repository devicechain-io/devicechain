// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The widget contract — the props every widget component receives.

import type { MeasurementStreamState, WidgetActions, WidgetInstance } from '@devicechain/dashboards';
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

// What a registered custom editor receives: the widget's options and a way to replace them.
// A placeholder shape — the host services an editor needs are refined with the authoring
// work; a registration only reserves the seam.
export interface WidgetEditorProps<O> {
  options: O;
  onChange: (next: O) => void;
}
