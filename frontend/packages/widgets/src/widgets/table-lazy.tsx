// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The measurement table's registration: descriptor eager, component lazy, from a chunk of its
// own, so a board with no table never downloads it. Table is also a public export; see
// alarm-lazy.tsx for what that means for a host's first download.

import { defineWidget } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { lazyChunk } from './lazy-chunk';

const chunk = lazyChunk<typeof import('./table')>(() => import('./table'));

export const loadTableWidget = chunk.load;

export const tableWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['table'],
  implementation: { kind: 'trusted-react', component: chunk.component('Table') },
});
