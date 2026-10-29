// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment node

// The building blocks moved into @devicechain/widget-sdk, and this package re-exports them so
// every existing `import { ... } from '@devicechain/widgets'` keeps resolving. A dropped name
// is a breaking change nothing inside this repo notices -- no file here imports its own index
// for these -- so the list is pinned, and each name must be the SDK's own object (a second
// copy of a context hook would read a context the host never provides).

import * as sdk from '@devicechain/widget-sdk';
import * as sdkCharts from '@devicechain/widget-sdk/echarts';
import { describe, expect, it } from 'vitest';

import * as widgets from './index';

const FROM_SDK_ROOT = [
  'WidgetFrame',
  'WidgetSubjectProvider',
  'WidgetSelectProvider',
  'useWidgetSelect',
  'WidgetTimeZoneProvider',
  'useWidgetTimeZone',
  'WidgetThemeRootProvider',
  'useWidgetThemeRoot',
  'WidgetCandidatesProvider',
  'useWidgetCandidates',
  'useFlashOnChange',
  'flashTextStyle',
  'flashBackgroundStyle',
  'FlashValue',
  'useMeasurementStream',
  'useAlarmStream',
  'useCommandStream',
  'useLocationStream',
  'useDatasourceAvailability',
  'useDashboardHub',
  'useHubConnectionState',
  'HubConnectionBadge',
  'useResolvedBindings',
  'useViewerBindings',
  'useSlotCandidates',
  'useCandidates',
  'css',
  'pickSample',
  'optBoolean',
  'fitValueFont',
] as const;

const FROM_SDK_CHARTS = ['EChart', 'buildLineOption', 'buildGaugeOption', 'resolveChartTheme', 'useChartTheme'] as const;

describe('@devicechain/widgets re-exports the SDK building blocks', () => {
  it.each(FROM_SDK_ROOT)('%s, as the SDK root exports it', (name) => {
    expect(sdk[name], `the SDK root no longer exports ${name}`).toBeDefined();
    expect((widgets as Record<string, unknown>)[name]).toBe(sdk[name]);
  });

  it.each(FROM_SDK_CHARTS)('%s, as the SDK /echarts entry exports it', (name) => {
    expect(sdkCharts[name], `the /echarts entry no longer exports ${name}`).toBeDefined();
    expect((widgets as Record<string, unknown>)[name]).toBe(sdkCharts[name]);
  });
});
