// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The data side of an alarm widget grouped by device type: a tab strip around the alarm table.
// It lives in the alarm chunk (alarm-widgets.ts) so a board without one never downloads it.
//
// TWO streams, never one per tab. A summary (the ACTIVE alarms in scope counted per device type and
// severity by device-management, in one grouped read) feeds the tab strip's types, counts and worst
// severities; the rows of the SELECTED tab are a second stream, filtered by type on the server, that
// re-subscribes only when the selection changes, so a tab's rows are not read until it is chosen.
// The two classify alarms by the same server-side device read, so a badge and its tab's rows agree.
// The selection is state here, above both streams, so neither stream's refresh can reset it.
//
// A source that cannot count by type (a recording) fails the summary or leaves its counts unset;
// the strip is then not drawn, and the table shows every alarm in scope, as an ungrouped one does.

import type {
  AlarmSubscription,
  AlarmTypeCount,
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

// foldTypeTabs: one tab per device type among the active-alarm counts, most alarming first (worst
// severity, then count, then name) so the stream does not reorder as counts move within a severity.
// An alarm with no type (a non-device originator, or a device the viewer cannot read) is counted by
// no type and belongs to All only. The SELECTED type stays in the strip even when its last active
// alarm has cleared, at a zero count.
export function foldTypeTabs(
  counts: readonly AlarmTypeCount[],
  selected: string | null,
  names: Map<string, string | null>,
): AlarmTypeTab[] {
  const byType = new Map<string, AlarmTypeTab>();
  for (const c of counts) {
    const type = c.deviceType;
    if (c.count <= 0) continue;
    names.set(type.token, type.name);
    const tab = byType.get(type.token) ?? { token: type.token, name: type.name, active: 0, worstSeverity: null };
    tab.active += c.count;
    if (tab.worstSeverity === null || severityRank(c.severity) < severityRank(tab.worstSeverity)) tab.worstSeverity = c.severity;
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
  const summary = useHeld(
    useAlarmStream(hub, {
      datasource: base.datasource,
      severity: base.severity,
      acknowledged: base.acknowledged,
      state: 'ACTIVE',
      // No rows are read in count mode; a page of one keeps a source that ignores the flag cheap.
      pageSize: 1,
      countByDeviceType: true,
    }),
    held,
    ready,
  );
  // A type's name is remembered once seen, so a selected tab keeps its label after its last active
  // alarm clears.
  const names = useRef(new Map<string, string | null>());
  const counts = summary.typeCounts;
  const tabs = useMemo(() => (counts ? foldTypeTabs(counts, selected, names.current) : null), [counts, selected]);
  // No counts (a source that cannot make them, a failed first read, or one still loading): no strip,
  // and no type filter either, so the table is every alarm in scope and never a tab nobody can see.
  const filter = tabs ? selected : null;
  const rows = useHeld(
    useAlarmStream(hub, { ...base, enrichDeviceType: true, ...(filter !== null ? { deviceType: filter } : {}) }),
    held,
    ready,
  );
  const typeTabs = useMemo<AlarmTypeTabs | undefined>(
    () => (tabs ? { tabs, selected, select: setSelected, loading: summary.loading } : undefined),
    [tabs, selected, summary.loading],
  );

  // Same rule as the plain alarm channel: the error pane only when there is nothing to show.
  if (rows.error && rows.alarms.length === 0 && rows.total === 0) {
    return <ErrorFrame widget={widget} error={rows.error} />;
  }
  return <Component widget={widget} data={{ ...rows, typeTabs }} actions={actions} />;
}
