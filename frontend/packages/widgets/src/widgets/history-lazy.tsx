// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The history table's registration: descriptor eager, component lazy, from a chunk of its own
// (history-widgets.ts), so a board without one never downloads it.

import { defineWidget } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { lazyChunk } from './lazy-chunk';

const chunk = lazyChunk<typeof import('./history-widgets')>(() => import('./history-widgets'));

export const loadHistoryWidgets = chunk.load;

export const historyTableWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['history-table'],
  implementation: { kind: 'trusted-react', component: chunk.component('HistoryTable') },
});
