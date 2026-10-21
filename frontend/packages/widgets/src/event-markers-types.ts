// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the event-marker feed (event-markers-feed.tsx, its own lazy chunk) hands the time-series
// chart. TYPES ONLY, so importing this from the chart costs the eager bundle nothing: the marker
// code itself never loads for a chart that has the markers off.

import type { ElementSize } from '@devicechain/widget-sdk';
import type { EChartOption } from '@devicechain/widget-sdk/echarts';
import type { ReactNode } from 'react';

export interface EventMarkersView {
  // Adds the markers to the chart's own option: a helper series carrying the markLines and
  // markArea, and the x-axis fixed to the range they were placed in. Pure; the chart's series are
  // untouched, so the markers cannot hide or move a reading.
  decorate: (option: EChartOption, size?: ElementSize) => EChartOption;
  // Rendered under the chart: the keyboard-reachable list of the same events behind a disclosure,
  // and the notes ("N more not shown", "Commands are not in this recording"). Null when there is
  // nothing to say.
  footer: ReactNode;
}
