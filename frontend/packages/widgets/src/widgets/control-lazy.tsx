// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command-control registrations: descriptor eager, component lazy, from a chunk of their own
// (control-widgets.ts) so a board without a slider or toggle never downloads the sending code.

import { defineWidget, type CommandStreamState } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { lazyChunk } from './lazy-chunk';

const chunk = lazyChunk<typeof import('./control-widgets'), CommandStreamState>(() => import('./control-widgets'));

export const loadControlWidgets = chunk.load;

export const commandSliderWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['command-slider'],
  implementation: { kind: 'trusted-react', component: chunk.component('CommandSlider') },
});

export const commandToggleWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['command-toggle'],
  implementation: { kind: 'trusted-react', component: chunk.component('CommandToggle') },
});
