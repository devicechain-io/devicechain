// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The level, indicator and radial registrations: descriptor eager, component lazy. The descriptors
// are the catalog (the editor, the validators and WIDGET_TYPES read them without rendering
// anything), so they stay in the entry; the seven components load on first mount, together,
// from one chunk (level-widgets.ts), so a board without one of them never downloads it.
//
// Until the chunk arrives the widget renders nothing inside its box. A chunk that fails to
// load is THROWN, so the widget's error boundary shows the failure: an empty tank that looks
// like a reading is the outcome this must never produce.

import { defineWidget } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { lazyChunk } from './lazy-chunk';

const chunk = lazyChunk(() => import('./level-widgets'));

export const loadLevelWidgets = chunk.load;

const lazyComponent = chunk.component;

export const linearGaugeWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['linear-gauge'],
  implementation: { kind: 'trusted-react', component: lazyComponent('LinearGauge') },
});

export const levelGaugeWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['level-gauge'],
  implementation: { kind: 'trusted-react', component: lazyComponent('LevelGauge') },
});

export const batteryWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['battery'],
  implementation: { kind: 'trusted-react', component: lazyComponent('Battery') },
});

export const statusLightWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['status-light'],
  implementation: { kind: 'trusted-react', component: lazyComponent('StatusLight') },
});

export const signalBarsWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['signal-bars'],
  implementation: { kind: 'trusted-react', component: lazyComponent('SignalBars') },
});

export const compassWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['compass'],
  implementation: { kind: 'trusted-react', component: lazyComponent('Compass') },
});

export const progressRingWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['progress-ring'],
  implementation: { kind: 'trusted-react', component: lazyComponent('ProgressRing') },
});
