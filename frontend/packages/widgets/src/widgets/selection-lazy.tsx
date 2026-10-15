// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The entity selector's registration: descriptor eager, component lazy, from a chunk of its own,
// so a board with no selector never downloads it. EntitySelector is also a public export; see
// alarm-lazy.tsx for what that means for a host's first download.

import { defineWidget } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { lazyChunk } from './lazy-chunk';

const chunk = lazyChunk<typeof import('./entity-selector'), null>(() => import('./entity-selector'));

export const loadSelectionWidgets = chunk.load;

export const entitySelectorWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['entity-selector'],
  implementation: { kind: 'trusted-react', component: chunk.component('EntitySelector') },
});
