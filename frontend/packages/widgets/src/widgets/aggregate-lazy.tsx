// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The aggregate and categorical registrations: descriptor eager, component lazy, from a chunk of
// their own (aggregate-widgets.ts), so a board without a stat card, bar chart or pie chart never
// downloads them. The bar and pie charts are SVG, so no chart engine type is loaded for them.

import { defineWidget, type AggregateStreamState } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { lazyChunk } from './lazy-chunk';

const chunk = lazyChunk<typeof import('./aggregate-widgets')>(() => import('./aggregate-widgets'));

export const loadAggregateWidgets = chunk.load;

export const statCardWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['stat-card'],
  implementation: { kind: 'trusted-react', component: chunk.component<AggregateStreamState>('StatCard') },
});

export const barChartWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['bar-chart'],
  implementation: { kind: 'trusted-react', component: chunk.component('BarChart') },
});

export const pieChartWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['pie-chart'],
  implementation: { kind: 'trusted-react', component: chunk.component('PieChart') },
});

export const fleetCountWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['fleet-count'],
  implementation: { kind: 'trusted-react', component: chunk.component('FleetCount') },
});

export const fleetTableWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['fleet-table'],
  implementation: { kind: 'trusted-react', component: chunk.component('FleetTable') },
});
