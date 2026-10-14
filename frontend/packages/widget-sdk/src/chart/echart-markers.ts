// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The threshold markers (markLine + markArea), split out of echart.tsx so they load only when
// a chart actually draws thresholds. Together they are ~7 KB gzip, and most boards -- every
// gauge, and every chart without thresholds -- never use them. echart.tsx imports this module
// DYNAMICALLY; a static import of it anywhere would fold it back into the initial bundle.

import { MarkAreaComponent, MarkLineComponent } from 'echarts/components';

export const MARKER_COMPONENTS = [MarkLineComponent, MarkAreaComponent];
