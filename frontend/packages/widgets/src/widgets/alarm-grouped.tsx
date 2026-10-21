// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The data side of an alarm widget grouped by device type: a tab strip around the alarm table.
// It lives in the alarm chunk (alarm-widgets.ts) so a board without one never downloads it.
//
// TWO streams, never one per tab. A summary (every ACTIVE alarm in scope, with its device type, in
// one read) feeds the tab strip's types, counts and worst severities; the rows of the SELECTED tab
// are a second stream that re-subscribes only when the selection changes, so a tab's rows are not
// read until it is chosen. The selection is state here, above both streams, so neither stream's
// refresh can reset it.

import type {
  AlarmRow,
  AlarmSubscription,
  AlarmTypeTab,
  AlarmTypeTabs,
  WidgetActions,
  WidgetDataSource,
  WidgetInstance,
} from '@devicechain/dashboards';
import { useAlarmStream, useWidgetTimeRange } from '@devicechain/widget-sdk';
import { useMemo, useRef, useState, type ComponentType } from 'react';

import type { useHeld as UseHeld } from '../use-held';
import { severityRank } from './severity';

// The most active alarms the summary reads. Past it the counts are lower bounds (`truncated`).
export const ALARM_TAB_SUMMARY_ROWS = 500;

// 🔴 NOTHING IS IMPORTED FROM THE EAGER GRAPH HERE, ONLY TYPES. The bundler gives modules reachable
// from the same entries one shared chunk, and this chunk's modules are reachable from the package
// entry (AlarmTable is a public export): a value import of something the connected widget also uses
// (the pause hold, the subscription policy) would join them to that chunk, which a host's first
// download then carries whole. So the connected widget hands those in.
export interface GroupedAlarmProps {
  hub: WidgetDataSource;
  widget: WidgetInstance;
  // The subscription the widget's descriptor derives (scope, filters, page size).
  base: AlarmSubscription;
  // Holds a stream's snapshot while the board is paused (use-held.ts).
  useHeld: typeof UseHeld;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Component: ComponentType<any>;
  actions: WidgetActions | undefined;
  ErrorFrame: ComponentType<{ widget: WidgetInstance; error: unknown }>;
}

// foldTypeTabs: one tab per device type present among the active alarms, most alarming first (worst
// severity, then count, then name) so the stream does not reorder as counts move within a severity.
// A row whose type is unknown (an unresolved originator, or a non-device) belongs to All only. The
// SELECTED type stays in the strip even when its last active alarm has cleared, at a zero count.
export function foldTypeTabs(
  alarms: readonly AlarmRow[],
  selected: string | null,
  names: Map<string, string | null>,
): AlarmTypeTab[] {
  const byType = new Map<string, AlarmTypeTab>();
  for (const a of alarms) {
    const type = a.deviceType;
    if (!type) continue;
    names.set(type.token, type.name);
    const tab = byType.get(type.token) ?? { token: type.token, name: type.name, active: 0, worstSeverity: null };
    tab.active += 1;
    if (tab.worstSeverity === null || severityRank(a.severity) < severityRank(tab.worstSeverity)) tab.worstSeverity = a.severity;
    byType.set(type.token, tab);
  }
  if (selected !== null && !byType.has(selected)) {
    byType.set(selected, { token: selected, name: names.get(selected) ?? null, active: 0, worstSeverity: null });
  }
  return [...byType.values()].sort(
    (x, y) =>
      severityRank(x.worstSeverity ?? '') - severityRank(y.worstSeverity ?? '') ||
      y.active - x.active ||
      (x.name ?? x.token).localeCompare(y.name ?? y.token),
  );
}

export function GroupedAlarm({ widget, hub, base, useHeld, Component, actions, ErrorFrame }: GroupedAlarmProps) {
  const range = useWidgetTimeRange();
  const held = range?.paused === true;
  const [selected, setSelected] = useState<string | null>(null);
  const ready = (s: { loading: boolean }) => !s.loading;
  const rows = useHeld(
    useAlarmStream(hub, { ...base, enrichDeviceType: true, ...(selected !== null ? { deviceType: selected } : {}) }),
    held,
    ready,
  );
  const summary = useHeld(
    useAlarmStream(hub, {
      datasource: base.datasource,
      severity: base.severity,
      acknowledged: base.acknowledged,
      state: 'ACTIVE',
      pageSize: ALARM_TAB_SUMMARY_ROWS,
      enrichDeviceType: true,
    }),
    held,
    ready,
  );
  // A type's name is remembered once seen, so a selected tab keeps its label after its last active
  // alarm clears.
  const names = useRef(new Map<string, string | null>());
  const tabs = useMemo(() => foldTypeTabs(summary.alarms, selected, names.current), [summary.alarms, selected]);
  const typeTabs = useMemo<AlarmTypeTabs>(
    () => ({
      tabs,
      selected,
      select: setSelected,
      loading: summary.loading,
      truncated: summary.total > summary.alarms.length,
    }),
    [tabs, selected, summary.loading, summary.total, summary.alarms.length],
  );

  // Same rule as the plain alarm channel: the error pane only when there is nothing to show.
  if (rows.error && rows.alarms.length === 0 && rows.total === 0) {
    return <ErrorFrame widget={widget} error={rows.error} />;
  }
  return <Component widget={widget} data={{ ...rows, typeTabs }} actions={actions} />;
}
