// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The presence registrations: descriptor eager, component lazy, from a chunk of their own
// (presence-widgets.ts), so a board without a device status card or an online doughnut never
// downloads them.

import { defineWidget } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { lazyChunk } from './lazy-chunk';

const chunk = lazyChunk<typeof import('./presence-widgets')>(() => import('./presence-widgets'));

export const loadPresenceWidgets = chunk.load;

export const deviceStatusWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['device-status'],
  implementation: { kind: 'trusted-react', component: chunk.component('DeviceStatus') },
});

export const onlineDoughnutWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['online-doughnut'],
  implementation: { kind: 'trusted-react', component: chunk.component('OnlineDoughnut') },
});
