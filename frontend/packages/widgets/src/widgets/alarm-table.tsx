// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// alarm-table — the raised alarms in the widget's scope (ADR-041), newest first, with
// a severity stripe per row. When the runtime supplies an action seam AND the viewer
// holds alarm:write, each active row gets Acknowledge / Clear controls; otherwise the
// table is read-only. Bound through the hub's alarm channel, so it renders identically
// from live data or the synthetic preview source.

import type { AlarmRow, AlarmTypeTab, WidgetActions } from '@devicechain/dashboards';
import { Fragment, useEffect, useId, useState, type CSSProperties, type ReactNode } from 'react';

import {
  type AlarmStreamState,
  css,
  fontVar,
  FlashValue,
  formatDateTime,
  optBoolean,
  optNumber,
  optString,
  useElementSize,
  useSeverityColors,
  useWidgetSelect,
  useWidgetTimeZone,
  WidgetFrame,
  type WidgetProps,
  type WidgetSelect,
} from '@devicechain/widget-sdk';

import { alarmStrings } from '../alarm-strings';
import { AlarmTypeTabsStrip, panelDomId, tabDomId } from './alarm-type-tabs';
import { severityColor, severityLabel } from './severity';

const cell: CSSProperties = { padding: '6px 10px', textAlign: 'left', whiteSpace: 'nowrap' };
const headCell: CSSProperties = {
  ...cell,
  position: 'sticky',
  top: 0,
  background: css('card'),
  fontSize: fontVar('xs'),
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
  color: css('muted-foreground'),
  borderBottom: `1px solid ${css('border')}`,
};

// statusLabel collapses the 4-state model (state × acknowledged) into the one signal an
// operator reads, matching the console's alarm badges: cleared wins regardless of the
// acknowledged flag; an active alarm is "Acknowledged" once owned, else "Active".
// Below this measured width the table drops its lowest-value columns (Status, Raised) rather
// than clipping or scrolling sideways: severity, originator, alarm and value are what an
// operator reads at a glance, and the text columns wrap instead of pushing the value off the
// right edge (a phone's single column is ~390 px). Unmeasured (first frame, jsdom) keeps every
// column on one line.
export const ALARM_TABLE_NARROW_PX = 480;

// A narrow table's text cells: tighter padding, and a long originator or alarm key wraps
// across lines rather than widening the table past its box. Identifiers break at their
// hyphens first (see hyphenBreaks); `break-word` splits inside a word only when a single
// segment is wider than the cell. `anywhere` is deliberately NOT used: it also lowers the
// column's min-content width, so a squeezed column was split mid-word ("engine-overhea|t")
// instead of at a hyphen.
const narrowCell: CSSProperties = {
  ...cell,
  padding: '6px 6px',
  whiteSpace: 'normal',
  overflowWrap: 'break-word',
  wordBreak: 'normal',
};

// hyphenBreaks renders an identifier with a break opportunity after each hyphen, so a
// hyphenated key ("tyre-pressure-low") wraps as "tyre-/pressure-/low". The <wbr> also keeps
// the column's min-content width at the longest segment, which is what lets the table
// shrink at 390 px without pushing the Value column off the edge.
export function hyphenBreaks(text: string): ReactNode {
  // Split after each hyphen, keeping it on the left part. Not a lookbehind regex: this is a
  // published package, and a lookbehind literal is a parse error in older WebKit.
  const parts = text.split('-').map((p, i, all) => (i < all.length - 1 ? `${p}-` : p));
  return parts.map((part, i) => (
    <Fragment key={i}>
      {i > 0 ? <wbr /> : null}
      {part}
    </Fragment>
  ));
}

// The tabs a type-grouping table shows. By default every device type with an active alarm; an
// authored `deviceTypes` list (`[{ token }]`) pins WHICH types and their ORDER instead, and a type
// not listed is under All only. A pinned type with no active alarm still gets its tab (at no badge),
// named by its token when no alarm has told us its name.
export function shownTabs(discovered: readonly AlarmTypeTab[], pinned: readonly string[]): AlarmTypeTab[] {
  if (pinned.length === 0) return [...discovered];
  const byToken = new Map(discovered.map((t) => [t.token, t]));
  const seen = new Set<string>();
  const out: AlarmTypeTab[] = [];
  for (const token of pinned) {
    if (seen.has(token)) continue;
    seen.add(token);
    out.push(byToken.get(token) ?? { token, name: null, active: 0, worstSeverity: null });
  }
  return out;
}

function pinnedTypes(widget: WidgetProps<AlarmStreamState>['widget']): string[] {
  const list = widget.options?.deviceTypes;
  if (!Array.isArray(list)) return [];
  return list.flatMap((item) => {
    const token = (item as { token?: unknown } | null)?.token;
    return typeof token === 'string' && token !== '' ? [token] : [];
  });
}

function statusLabel(alarm: AlarmRow): string {
  if (alarm.state === 'CLEARED') return 'Cleared';
  return alarm.acknowledged ? 'Acknowledged' : 'Active';
}

export function AlarmTable({ widget, data, actions }: WidgetProps<AlarmStreamState>) {
  const { alarms, total, loading } = data;
  // Actions render only when the runtime supplies a seam AND the viewer may write.
  const canAct = actions?.can('alarm:write') ?? false;
  const [sizeRef, size] = useElementSize<HTMLDivElement>();
  const severity = useSeverityColors();
  const narrow = size.width > 0 && size.width < ALARM_TABLE_NARROW_PX;
  // Grouped by device type: a tab strip above the table. `typeTabs` is set only for a table whose
  // descriptor groups, so an ungrouped one runs none of this and renders exactly as it always has.
  const typeTabs = data.typeTabs;
  const tabs = typeTabs ? shownTabs(typeTabs.tabs, pinnedTypes(widget)) : [];
  const stripShown = typeTabs !== undefined && tabs.length > 0;
  const selectTab = typeTabs?.select;
  const selectedType = typeTabs?.selected ?? null;
  // A selection the pin dropped must not leave the table filtered to a tab the viewer cannot see.
  const dropped = stripShown && selectedType !== null && !tabs.some((t) => t.token === selectedType);
  useEffect(() => {
    if (dropped) selectTab?.(null);
  }, [dropped, selectTab]);
  const idBase = useId();
  // The type column is redundant inside a type's own tab, and too wide for a phone.
  const typeColumn = stripShown && selectedType === null && !narrow;
  const columns = 5 + (narrow ? 0 : 2) + (typeColumn ? 1 : 0) + (canAct ? 1 : 0);
  const text = narrow ? narrowCell : cell;
  // Round the triggering value to a fixed precision when configured; unset keeps the raw
  // value (formatValue's default), matching the table widget. An alarm's raw lastValue is
  // otherwise a full-width float (e.g. 31.19073834583732).
  const precision = optNumber(widget.options, 'precision');
  // Opt-in directional flash on the triggering value, per row (keyed by alarm token).
  const flash = optBoolean(widget.options, 'flashOnChange');
  // Drill: when a `selectionTarget` slot is configured AND the renderer wired a selection
  // callback (view mode, a host that supports it), a device originator becomes a link that
  // re-points that slot — driving the per-device widgets to the clicked thermostat
  // (ADR-039 selection amendment). Absent either, the originator is plain text.
  const select = useWidgetSelect();
  const timeZone = useWidgetTimeZone();
  const selectionTarget = optString(widget.options, 'selectionTarget');
  const drillTo = select && selectionTarget ? { select, slot: selectionTarget } : undefined;

  return (
    <WidgetFrame title={optString(widget.options, 'title')} flush>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
        {stripShown && typeTabs ? (
          <AlarmTypeTabsStrip
            tabs={tabs}
            selected={dropped ? null : selectedType}
            onSelect={typeTabs.select}
            truncated={typeTabs.truncated}
            idBase={idBase}
          />
        ) : null}
        <div
          ref={sizeRef}
          style={{ flex: 1, minHeight: 0, overflow: 'auto' }}
          {...(stripShown
            ? { role: 'tabpanel', id: panelDomId(idBase), 'aria-labelledby': tabDomId(idBase, dropped ? null : selectedType) }
            : {})}
        >
          <table
            style={{
              width: '100%',
              borderCollapse: 'collapse',
              fontSize: fontVar('md'),
              color: css('foreground'),
              fontVariantNumeric: 'tabular-nums',
            }}
          >
            <thead>
              <tr>
                <th style={{ ...headCell, width: 4, padding: 0 }} aria-hidden="true" />
                <th scope="col" style={headCell}>
                  Severity
                </th>
                {narrow ? null : (
                  <th scope="col" style={headCell}>
                    Status
                  </th>
                )}
                <th scope="col" style={headCell}>
                  Originator
                </th>
                {typeColumn ? (
                  <th scope="col" style={headCell}>
                    {alarmStrings().deviceType}
                  </th>
                ) : null}
                <th scope="col" style={headCell}>
                  Alarm
                </th>
                <th scope="col" style={{ ...headCell, textAlign: 'right' }}>
                  Value
                </th>
                {narrow ? null : (
                  <th scope="col" style={headCell}>
                    Raised
                  </th>
                )}
                {canAct ? (
                  <th scope="col" style={{ ...headCell, textAlign: 'right' }}>
                    Actions
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody>
              {alarms.length === 0 ? (
                <tr>
                  <td colSpan={columns} style={{ ...cell, textAlign: 'center', color: css('muted-foreground') }}>
                    {loading ? 'Loading…' : 'No alarms'}
                  </td>
                </tr>
              ) : (
                alarms.map((alarm) => {
                  const color = severityColor(severity, alarm.severity);
                  return (
                    <tr key={alarm.token} style={{ borderBottom: `1px solid ${css('border')}` }}>
                      <td aria-hidden="true" style={{ padding: 0, width: 4, background: color }} />
                      <td style={cell}>
                        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                          <span
                            aria-hidden="true"
                            style={{ width: 8, height: 8, borderRadius: '50%', background: color, flexShrink: 0 }}
                          />
                          {severityLabel(alarm.severity)}
                        </span>
                      </td>
                      {narrow ? null : <td style={{ ...cell, color: css('muted-foreground') }}>{statusLabel(alarm)}</td>}
                      <td style={text}>
                        <OriginatorCell alarm={alarm} drillTo={drillTo} />
                      </td>
                      {typeColumn ? (
                        <td style={{ ...cell, color: css('muted-foreground') }}>
                          {alarm.deviceType ? (alarm.deviceType.name ?? alarm.deviceType.token) : '—'}
                        </td>
                      ) : null}
                      <td style={text}>
                        {hyphenBreaks(alarm.alarmKey)}
                      </td>
                      <td style={{ ...cell, textAlign: 'right' }}>
                        <FlashValue
                          value={alarm.lastValue}
                          precision={precision}
                          enabled={flash}
                          identity={alarm.token}
                        />
                      </td>
                      {narrow ? null : (
                        <td style={{ ...cell, color: css('muted-foreground') }}>
                          {formatDateTime(alarm.raisedTime, timeZone)}
                        </td>
                      )}
                      {canAct ? (
                        <td style={{ ...cell, textAlign: 'right' }}>
                          {/* actions is defined whenever canAct is true. */}
                          <AlarmRowActions alarm={alarm} actions={actions as WidgetActions} />
                        </td>
                      ) : null}
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
        {total > alarms.length ? (
          <div
            style={{
              padding: '4px 10px',
              fontSize: fontVar('xs'),
              color: css('muted-foreground'),
              borderTop: `1px solid ${css('border')}`,
            }}
          >
            Showing {alarms.length} of {total}
          </div>
        ) : null}
      </div>
    </WidgetFrame>
  );
}

// OriginatorCell renders the alarm's originator. When drill is wired AND the originator
// is a device (it carries a token), it is a link that re-points the target slot to that
// device; otherwise it is plain text (the token, or the originator type when tokenless).
// The drill target is a device binding — a building-scoped alarm table's originators are
// already members of the context, so the drill stays in-context.
function OriginatorCell({
  alarm,
  drillTo,
}: {
  alarm: AlarmRow;
  drillTo: { select: WidgetSelect; slot: string } | undefined;
}) {
  if (drillTo && alarm.originatorType === 'device' && alarm.originatorToken) {
    const token = alarm.originatorToken;
    return (
      <button
        type="button"
        onClick={() => drillTo.select({ slot: drillTo.slot, binding: { kind: 'device', deviceToken: token } })}
        title={`Show ${token} in the linked widgets`}
        style={{
          padding: 0,
          border: 'none',
          background: 'none',
          font: 'inherit',
          color: css('primary'),
          cursor: 'pointer',
          textDecoration: 'underline',
          textUnderlineOffset: 2,
        }}
      >
        {hyphenBreaks(token)}
      </button>
    );
  }
  return <>{hyphenBreaks(alarm.originatorToken ?? alarm.originatorType)}</>;
}

const actionButton: CSSProperties = {
  padding: '2px 8px',
  fontSize: fontVar('sm'),
  lineHeight: 1.4,
  borderRadius: 4,
  border: `1px solid ${css('border')}`,
  background: css('card'),
  color: css('foreground'),
  cursor: 'pointer',
};

// AlarmRowActions owns the pending/error state for one row's Acknowledge / Clear. Only
// an ACTIVE alarm is actionable (a cleared — or any future non-active — state shows
// "—"); an acknowledged one still shows Clear. After a successful mutation the hub
// reconciles the table, so the row updates itself.
function AlarmRowActions({ alarm, actions }: { alarm: AlarmRow; actions: WidgetActions }) {
  const severity = useSeverityColors();
  const [pending, setPending] = useState<'ack' | 'clear' | null>(null);
  const [failed, setFailed] = useState(false);

  if (alarm.state !== 'ACTIVE') {
    return <span style={{ color: css('muted-foreground') }}>—</span>;
  }

  const run = (kind: 'ack' | 'clear', op: () => Promise<void>) => {
    setPending(kind);
    setFailed(false);
    // Call op() synchronously (so an immediate dispatch is observable) but guard a
    // synchronous throw too, so a misbehaving seam can't leave the row stuck-disabled.
    let promise: Promise<void>;
    try {
      promise = op();
    } catch {
      setFailed(true);
      setPending(null);
      return;
    }
    promise.catch(() => setFailed(true)).finally(() => setPending(null));
  };

  return (
    <span style={{ display: 'inline-flex', gap: 6, justifyContent: 'flex-end', alignItems: 'center' }}>
      {failed ? (
        <span
          role="img"
          aria-label="Action failed — retry"
          title="Action failed — retry"
          style={{ color: severityColor(severity, 'CRITICAL'), fontSize: fontVar('sm') }}
        >
          ⚠
        </span>
      ) : null}
      {!alarm.acknowledged ? (
        <button
          type="button"
          style={{ ...actionButton, opacity: pending ? 0.6 : 1 }}
          disabled={pending !== null}
          aria-busy={pending === 'ack'}
          aria-label={`Acknowledge alarm ${alarm.alarmKey}`}
          onClick={() => run('ack', () => actions.acknowledgeAlarm(alarm.token))}
        >
          {pending === 'ack' ? '…' : 'Ack'}
        </button>
      ) : null}
      <button
        type="button"
        style={{ ...actionButton, opacity: pending ? 0.6 : 1 }}
        disabled={pending !== null}
        aria-busy={pending === 'clear'}
        aria-label={`Clear alarm ${alarm.alarmKey}`}
        onClick={() => run('clear', () => actions.clearAlarm(alarm.token))}
      >
        {pending === 'clear' ? '…' : 'Clear'}
      </button>
    </span>
  );
}
