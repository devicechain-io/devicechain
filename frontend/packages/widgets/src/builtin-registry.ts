// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The built-in widgets as registrations: each portable descriptor (builtin-descriptors.ts)
// paired with the component today's per-channel registries hold, declared through the SAME
// public `defineWidget` a third-party package uses — there is no privileged internal path.
//
// The components are taken FROM the per-channel registries rather than imported again, so
// until the built-in cutover retires those tables there is exactly one place that says which
// component renders which widget (builtin-registry.test.ts asserts the identity).

import { createWidgetRegistry, defineWidget, type WidgetRegistry } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors as d } from './builtin-descriptors';
import {
  ALARM_WIDGET_REGISTRY,
  CONTROL_WIDGET_REGISTRY,
  LOCATION_WIDGET_REGISTRY,
  SELECTION_WIDGET_REGISTRY,
  WIDGET_REGISTRY,
} from './registry';

const trusted = 'trusted-react' as const;

export const builtinWidgetDefinitions = [
  defineWidget({ descriptor: d['timeseries-chart'], implementation: { kind: trusted, component: WIDGET_REGISTRY['timeseries-chart'] } }),
  defineWidget({ descriptor: d['latest-card'], implementation: { kind: trusted, component: WIDGET_REGISTRY['latest-card'] } }),
  defineWidget({ descriptor: d.gauge, implementation: { kind: trusted, component: WIDGET_REGISTRY.gauge } }),
  defineWidget({ descriptor: d.table, implementation: { kind: trusted, component: WIDGET_REGISTRY.table } }),
  defineWidget({ descriptor: d.label, implementation: { kind: trusted, component: WIDGET_REGISTRY.label } }),
  defineWidget({ descriptor: d.image, implementation: { kind: trusted, component: WIDGET_REGISTRY.image } }),
  defineWidget({ descriptor: d['alarm-table'], implementation: { kind: trusted, component: ALARM_WIDGET_REGISTRY['alarm-table'] } }),
  defineWidget({ descriptor: d['alarm-count'], implementation: { kind: trusted, component: ALARM_WIDGET_REGISTRY['alarm-count'] } }),
  defineWidget({ descriptor: d['command-button'], implementation: { kind: trusted, component: CONTROL_WIDGET_REGISTRY['command-button'] } }),
  defineWidget({ descriptor: d['entity-selector'], implementation: { kind: trusted, component: SELECTION_WIDGET_REGISTRY['entity-selector'] } }),
  defineWidget({ descriptor: d.map, implementation: { kind: trusted, component: LOCATION_WIDGET_REGISTRY.map } }),
] as const;

// A fresh, sealed registry holding exactly the built-ins. Every call returns an independent
// instance; a host composing extensions passes `[...builtinWidgetDefinitions, theirs]` to
// createWidgetRegistry from @devicechain/widget-sdk instead.
export function createBuiltinWidgetRegistry(): WidgetRegistry {
  return createWidgetRegistry(builtinWidgetDefinitions);
}
