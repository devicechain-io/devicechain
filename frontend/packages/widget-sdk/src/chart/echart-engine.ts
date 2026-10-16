// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The chart engine: Apache ECharts (and zrender beneath it), registered with ONLY the chart types
// and components the option builders emit -- line + gauge, grid/tooltip/legend, canvas renderer --
// so tree-shaking keeps the rest of ECharts out of the bundle.
//
// 🔴 THIS MODULE IS LOADED LAZILY, AND IT IS THE ONLY MODULE THAT IMPORTS ECHARTS AT RUNTIME.
// echart.tsx reaches it through `import('./echart-engine')`, so a host bundler emits ECharts as ONE
// chunk, fetched the first time a chart mounts (or earlier, through loadChartEngine, which
// preloadWidgets calls for a board that holds a chart). At ~170 KB gzip it is the largest single
// dependency of a board, and a board with no chart never downloads it. A STATIC import of this
// module, or of `echarts/*`, from anywhere in the SDK folds the whole engine back into every host's
// first download: dist-shape.test.ts asserts the boundary on the built artifact.
//
// The threshold markers are imported from HERE, not from echart.tsx, for the same reason: their
// chunk then depends on this one, which is already loaded whenever it is fetched, so the bundler
// has no ECharts code that two independent lazy chunks share and no reason to split the engine.

import { GaugeChart, LineChart } from 'echarts/charts';
import { GridComponent, LegendPlainComponent, TooltipComponent } from 'echarts/components';
import * as echarts from 'echarts/core';
import { CanvasRenderer } from 'echarts/renderers';

// 🔴 EVERY COMPONENT AN OPTION BUILDER EMITS MUST BE REGISTERED -- here, or (the threshold
// markers) by registerThresholdMarkers below. ECharts ignores an option key whose component is
// not registered -- silently, no warning -- so a `legend` or a `markLine` the builder sets
// draws nothing. The legend was dropped this way for as long as multi-series charts existed.
// echart.registration.test.tsx renders each through a real ECharts instance, so a builder
// that grows a new component fails there until it is registered.
//
// The PLAIN legend, not LegendComponent: that one also installs the scrollable legend, which
// no builder asks for (~1.8 KB gzip).
echarts.use([LineChart, GaugeChart, GridComponent, TooltipComponent, LegendPlainComponent, CanvasRenderer]);

export const init = echarts.init;

// markLine / markArea are registered on first use (see echart-markers.ts for why).
let markers: Promise<void> | undefined;

export function registerThresholdMarkers(): Promise<void> {
  markers ??= import('./echart-markers').then(
    (m) => {
      echarts.use(m.MARKER_COMPONENTS);
    },
    (err: unknown) => {
      markers = undefined; // a failed chunk load is retried by the next chart that needs it
      throw err;
    },
  );
  return markers;
}
