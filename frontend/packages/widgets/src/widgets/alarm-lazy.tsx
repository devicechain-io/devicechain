// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm registrations: descriptor eager, component lazy, from a chunk of their own
// (alarm-widgets.ts), so a board without an alarm table or alarm count never downloads them.
//
// AlarmTable and AlarmCount are also public exports of this package. That keeps them in this
// package's own entry graph, and a host bundler drops them from its first download only because
// package.json declares the modules free of side effects (see dist-shape.test.ts).

import { defineWidget, type AlarmStreamState } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { lazyChunk } from './lazy-chunk';

const chunk = lazyChunk<typeof import('./alarm-widgets'), AlarmStreamState>(() => import('./alarm-widgets'));

export const loadAlarmWidgets = chunk.load;

export const alarmTableWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['alarm-table'],
  implementation: { kind: 'trusted-react', component: chunk.component('AlarmTable') },
});

export const alarmSeverityWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['alarm-severity'],
  implementation: { kind: 'trusted-react', component: chunk.component('AlarmSeverity') },
});

export const alarmCountWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['alarm-count'],
  implementation: { kind: 'trusted-react', component: chunk.component('AlarmCount') },
});
