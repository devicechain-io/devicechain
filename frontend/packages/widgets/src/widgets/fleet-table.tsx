// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// fleet-table -- one row per device of an anchor, one column per measurement. Each cell is that
// DEVICE's own latest reading (the fleet feed keeps the device beside the value; the measurement
// channel's name-only `latest` would collapse ten thermostats into one "temperature").
//
//   - a band from the column's thresholds colours the value; the normal band keeps the table's own
//     colour, as everywhere else, so a table of healthy devices is calm;
//   - each cell shows the reading's age, and is called stale only against the configured
//     `staleAfterSeconds` (the freshness convention: no threshold, no stale mark). With
//     `ageLagSeconds` a cell shows its age only when its reading lags the NEWEST reading in the
//     table by at least that much (or is stale): on a fleet that reports every few seconds every
//     age reads "a few seconds ago", which is noise that doubles each row, and the frame's footer
//     already carries the fleet's own age. The lag is measured between readings, not against the
//     viewer's clock, so a played-back run (whose readings are all days old) shows the machine
//     that fell silent and no other;
//   - a cell with no number says WHY: still loading, the read failed, the device sent nothing in
//     the range, or its profile does not declare this measurement;
//   - columns sort from their headings (real buttons, so the keyboard works), the device column
//     drills like the alarm table's originator, and a long fleet is paged. A fleet beyond what the
//     resolver reads says "Showing N of M devices" rather than pass a subset off as the fleet;
//   - on a narrow board the table scrolls sideways inside its widget and keeps the device column
//     in view.

import type { FleetMember } from '@devicechain/dashboards';
import { useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react';

import {
  ageSeconds,
  bandAt,
  css,
  fontVar,
  formatAge,
  formatValue,
  isStale,
  levelColor,
  optNumber,
  optBoolean,
  optString,
  parseThresholds,
  resolveStaleAfterSeconds,
  spaceVar,
  useSeverityColors,
  useWidgetScale,
  useWidgetSelect,
  useWidgetTimeRange,
  WidgetFrame,
  type Threshold,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import {
  DEFAULT_PAGE_SIZE,
  cellFor,
  clampPage,
  columnNames,
  deviceLabel,
  pageCount,
  sortMembers,
  type Cell,
  type SortKey,
} from '../fleet-model';
import { fleetExport } from '../export-rows';
import { fleetStrings, type FleetStrings } from '../fleet-strings';
import { LevelEmpty } from '../level-readout';

// How often the ages are recomputed. Five seconds reads as live for "12 sec. ago" and costs a
// page of cells one render per tick, not per second.
const AGE_TICK_MS = 5_000;

interface ColumnFormat {
  label?: string;
  unit?: string;
  precision?: number;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

// The per-column formatting, by measurement name. Read defensively: a hand-edited definition may
// carry anything, and an entry that cannot be read drops out rather than take the table down.
export function columnFormats(raw: unknown): Map<string, ColumnFormat> {
  const out = new Map<string, ColumnFormat>();
  if (!Array.isArray(raw)) return out;
  for (const item of raw) {
    if (!isRecord(item) || typeof item.measurement !== 'string' || item.measurement === '') continue;
    const f: ColumnFormat = {};
    if (typeof item.label === 'string' && item.label !== '') f.label = item.label;
    if (typeof item.unit === 'string' && item.unit !== '') f.unit = item.unit;
    if (typeof item.precision === 'number' && Number.isInteger(item.precision) && item.precision >= 0 && item.precision <= 100) {
      f.precision = item.precision;
    }
    out.set(item.measurement, f);
  }
  return out;
}

// The bands of each column: the thresholds list, grouped by the measurement each entry names.
export function columnBands(raw: unknown): Map<string, Threshold[]> {
  const grouped = new Map<string, unknown[]>();
  if (Array.isArray(raw)) {
    for (const item of raw) {
      if (!isRecord(item) || typeof item.measurement !== 'string') continue;
      const list = grouped.get(item.measurement) ?? [];
      list.push(item);
      grouped.set(item.measurement, list);
    }
  }
  const out = new Map<string, Threshold[]>();
  for (const [name, items] of grouped) out.set(name, parseThresholds(items));
  return out;
}

const cellBox: CSSProperties = { padding: `${spaceVar(1)} ${spaceVar(3)}`, textAlign: 'left', whiteSpace: 'nowrap', verticalAlign: 'top' };
const headBox: CSSProperties = {
  ...cellBox,
  fontSize: fontVar('xs'),
  fontWeight: 600,
  letterSpacing: '0.04em',
  color: css('muted-foreground'),
  borderBottom: `1px solid ${css('border')}`,
  background: css('card'),
  position: 'sticky',
  top: 0,
  zIndex: 1,
};
// The device column stays in view while the measurement columns scroll under it.
const stickyFirst: CSSProperties = { position: 'sticky', left: 0, background: css('card') };
const plainButton: CSSProperties = {
  padding: 0,
  border: 'none',
  background: 'none',
  font: 'inherit',
  color: 'inherit',
  cursor: 'pointer',
  textAlign: 'left',
};

export function FleetTable({ widget, data }: WidgetProps) {
  const strings = fleetStrings();
  const fleet = data.members;
  const title = optString(widget.options, 'title');

  if (!fleet || fleet.status === 'loading') {
    return <Frame title={title}><LevelEmpty message={strings.loading} /></Frame>;
  }
  if (fleet.status === 'unbound') return <Frame title={title}><LevelEmpty message={strings.unbound} /></Frame>;
  if (fleet.status === 'forbidden') return <Frame title={title}><LevelEmpty message={strings.forbidden} /></Frame>;
  if (fleet.status === 'unsupported') return <Frame title={title}><LevelEmpty message={strings.unsupported} /></Frame>;
  if (fleet.members.length === 0) return <Frame title={title}><LevelEmpty message={strings.noMembers} /></Frame>;
  return <Table widget={widget} fleet={fleet} strings={strings} />;
}

// Below this width (px of the widget body) the table becomes a card per device.
export const STACK_BELOW_PX = 480;

function Responsive({ wide, narrow }: { wide: ReactNode; narrow: ReactNode }) {
  const { width } = useWidgetScale().size;
  // An unmeasured body (width 0: before the first layout, or under a test renderer) takes the table.
  return width > 0 && width < STACK_BELOW_PX ? narrow : wide;
}

function Frame({ title, children }: { title: string | undefined; children: ReactNode }) {
  return <WidgetFrame title={title} flush>{children}</WidgetFrame>;
}

function Table({
  widget,
  fleet,
  strings,
}: {
  widget: WidgetProps['widget'];
  fleet: NonNullable<WidgetProps['data']['members']>;
  strings: FleetStrings;
}) {
  const colors = useSeverityColors();
  const range = useWidgetTimeRange();
  const select = useWidgetSelect();
  const selectionTarget = optString(widget.options, 'selectionTarget');
  const drillTo = select && selectionTarget ? { select, slot: selectionTarget } : undefined;

  const columns = useMemo(() => columnNames(widget.datasource?.measurements ?? [], fleet.members), [widget.datasource, fleet.members]);
  const formats = useMemo(() => columnFormats(widget.options?.columns), [widget.options?.columns]);
  const bands = useMemo(() => columnBands(widget.options?.thresholds), [widget.options?.thresholds]);
  const staleAfter = resolveStaleAfterSeconds(widget.options);
  const ageLag = positiveSeconds(optNumber(widget.options, 'ageLagSeconds'));
  // The newest reading among the cells the table shows: what a cell's lag is measured from.
  const newest = useMemo(() => (ageLag === undefined ? undefined : newestShown(fleet.members, columns)), [ageLag, fleet.members, columns]);
  const pageSize = optNumber(widget.options, 'pageSize') ?? DEFAULT_PAGE_SIZE;

  // The author's order is the starting point; a click on a heading overrides it for this viewer.
  const authored: SortKey = { by: optString(widget.options, 'sortBy') || null, descending: optBoolean(widget.options, 'sortDescending') === true };
  const [picked, setPicked] = useState<SortKey | undefined>(undefined);
  const sort = picked ?? authored;
  const [page, setPage] = useState(0);

  // Ages move with the clock, and stand still at the freeze instant while the board is paused.
  const [tick, setTick] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setTick(Date.now()), AGE_TICK_MS);
    return () => clearInterval(id);
  }, []);
  const now = range?.paused ? range.pausedAt ?? tick : tick;

  const sorted = useMemo(() => sortMembers(fleet.members, sort), [fleet.members, sort.by, sort.descending]); // eslint-disable-line react-hooks/exhaustive-deps
  const shownPage = clampPage(page, sorted.length, pageSize);
  const rows = sorted.slice(shownPage * pageSize, (shownPage + 1) * pageSize);
  const pages = pageCount(sorted.length, pageSize);

  const sortOn = (by: string | null): void => {
    setPicked({ by, descending: sort.by === by ? !sort.descending : false });
    setPage(0);
  };
  const ariaSort = (by: string | null): 'ascending' | 'descending' | 'none' =>
    sort.by === by ? (sort.descending ? 'descending' : 'ascending') : 'none';
  const arrow = (by: string | null): string => (sort.by === by ? (sort.descending ? ' ▼' : ' ▲') : '');

  const wide = (
    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: fontVar('md'), color: css('foreground') }}>
      <thead>
        <tr>
          <th scope="col" aria-sort={ariaSort(null)} style={{ ...headBox, ...stickyFirst, zIndex: 2 }}>
            <button type="button" style={plainButton} onClick={() => sortOn(null)} title={strings.sortBy(strings.device)}>
              {strings.device}
              {arrow(null)}
            </button>
          </th>
          {columns.map((name) => {
            const f = formats.get(name);
            const heading = (f?.label ?? name) + (f?.unit ? ` (${f.unit})` : '');
            return (
              <th key={name} scope="col" aria-sort={ariaSort(name)} style={headBox}>
                <button type="button" style={plainButton} onClick={() => sortOn(name)} title={strings.sortBy(heading)}>
                  {heading}
                  {arrow(name)}
                </button>
              </th>
            );
          })}
        </tr>
      </thead>
      <tbody>
        {rows.map((member) => {
          const label = deviceLabel(member);
          return (
            <tr key={member.token} data-device={member.token} style={{ borderBottom: `1px solid ${css('border')}` }}>
              <th scope="row" style={{ ...cellBox, ...stickyFirst, fontWeight: 500 }}>
                {drillTo ? (
                  <button
                    type="button"
                    style={{ ...plainButton, color: css('primary'), textDecoration: 'underline', textUnderlineOffset: 2 }}
                    title={strings.drill(label)}
                    onClick={() => drillTo.select({ slot: drillTo.slot, binding: { kind: 'device', deviceToken: member.token } })}
                  >
                    {label}
                  </button>
                ) : (
                  label
                )}
              </th>
              {columns.map((name) => (
                <td key={name} data-measurement={name} style={cellBox}>
                  <CellView
                    cell={cellFor(member, name)}
                    format={formats.get(name)}
                    bands={bands.get(name)}
                    colors={colors}
                    staleAfter={staleAfter}
                    ageLag={ageLag}
                    newest={newest}
                    now={now}
                    strings={strings}
                  />
                </td>
              ))}
            </tr>
          );
        })}
      </tbody>
    </table>
  );

  // Narrow, the table gives way to a card per device: a column cut off at the right edge is unread
  // on a wall display, where nobody scrolls sideways, so the narrow layout never overflows.
  const narrow = (
    <div data-fleet-stacked="" style={{ color: css('foreground'), fontSize: fontVar('md') }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: spaceVar(2), padding: `${spaceVar(2)} ${spaceVar(3)}` }}>
        {[null, ...columns].map((by) => {
          const f = by === null ? undefined : formats.get(by);
          const heading = by === null ? strings.device : (f?.label ?? by);
          return (
            <button
              key={by ?? ''}
              type="button"
              aria-pressed={sort.by === by}
              style={{ ...plainButton, fontSize: fontVar('xs'), fontWeight: 600, color: css('muted-foreground'), overflowWrap: 'anywhere' }}
              onClick={() => sortOn(by)}
              title={strings.sortBy(heading)}
            >
              {heading}
              {arrow(by)}
            </button>
          );
        })}
      </div>
      {rows.map((member) => {
        const label = deviceLabel(member);
        return (
          <div key={member.token} data-device={member.token} style={{ padding: `${spaceVar(2)} ${spaceVar(3)}`, borderTop: `1px solid ${css('border')}` }}>
            <div style={{ fontWeight: 500, overflowWrap: 'anywhere' }}>
              {drillTo ? (
                <button
                  type="button"
                  style={{ ...plainButton, color: css('primary'), textDecoration: 'underline', textUnderlineOffset: 2, overflowWrap: 'anywhere' }}
                  title={strings.drill(label)}
                  onClick={() => drillTo.select({ slot: drillTo.slot, binding: { kind: 'device', deviceToken: member.token } })}
                >
                  {label}
                </button>
              ) : (
                label
              )}
            </div>
            {columns.map((name) => {
              const f = formats.get(name);
              return (
                <div key={name} data-measurement={name} style={{ display: 'flex', justifyContent: 'space-between', gap: spaceVar(3), alignItems: 'baseline' }}>
                  <span style={{ color: css('muted-foreground'), fontSize: fontVar('sm'), overflowWrap: 'anywhere' }}>{f?.label ?? name}</span>
                  <span style={{ textAlign: 'right', overflowWrap: 'anywhere' }}>
                    <CellView
                      cell={cellFor(member, name)}
                      format={f}
                      bands={bands.get(name)}
                      colors={colors}
                      staleAfter={staleAfter}
                      ageLag={ageLag}
                      newest={newest}
                      now={now}
                      strings={strings}
                      wrap
                    />
                  </span>
                </div>
              );
            })}
          </div>
        );
      })}
    </div>
  );

  return (
    <WidgetFrame
      title={optString(widget.options, 'title')}
      flush
      exportRows={() =>
        fleetExport({
          subject: optString(widget.options, 'title') ?? 'fleet',
          members: fleet.members,
          total: fleet.total,
          columns: columns.map((name) => {
            const f = formats.get(name);
            return { name, heading: (f?.label ?? name) + (f?.unit ? ` (${f.unit})` : ''), precision: f?.precision };
          }),
          sort,
        })
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
        {/* A focusable scroll region: a keyboard user can scroll a wide table sideways. */}
        <div
          role="region"
          aria-label={optString(widget.options, 'title') ?? strings.device}
          tabIndex={0}
          data-fleet-scroll=""
          style={{ flex: 1, minHeight: 0, overflow: 'auto' }}
        >
          <Responsive wide={wide} narrow={narrow} />
        </div>
        {(fleet.truncated || pages > 1) && (
          <div
            data-fleet-footer=""
            style={{
              display: 'flex',
              flexWrap: 'wrap',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: spaceVar(2),
              padding: `${spaceVar(1)} ${spaceVar(3)}`,
              fontSize: fontVar('sm'),
              color: css('muted-foreground'),
              borderTop: `1px solid ${css('border')}`,
            }}
          >
            <span data-fleet-truncated={fleet.truncated ? '' : undefined}>
              {fleet.truncated ? strings.truncated(fleet.members.length, fleet.total) : ''}
            </span>
            {pages > 1 && (
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: spaceVar(2) }}>
                <button type="button" style={pagerButton} disabled={shownPage === 0} onClick={() => setPage(shownPage - 1)} aria-label={strings.previous}>
                  ‹
                </button>
                <span>{strings.range(shownPage * pageSize + 1, shownPage * pageSize + rows.length, sorted.length)}</span>
                <button type="button" style={pagerButton} disabled={shownPage >= pages - 1} onClick={() => setPage(shownPage + 1)} aria-label={strings.next}>
                  ›
                </button>
              </span>
            )}
          </div>
        )}
      </div>
    </WidgetFrame>
  );
}

// A seconds option that means something: a positive finite number, else unset.
function positiveSeconds(v: number | undefined): number | undefined {
  return v !== undefined && Number.isFinite(v) && v > 0 ? v : undefined;
}

// The newest reading time (epoch ms) among the shown columns of every member, or undefined.
export function newestShown(members: readonly FleetMember[], columns: readonly string[]): number | undefined {
  let newest: number | undefined;
  for (const m of members) {
    for (const name of columns) {
      const t = m.latest[name]?.occurredTime;
      const at = t ? Date.parse(t) : Number.NaN;
      if (!Number.isNaN(at) && (newest === undefined || at > newest)) newest = at;
    }
  }
  return newest;
}

const pagerButton: CSSProperties = {
  minWidth: '2em',
  padding: `0 ${spaceVar(2)}`,
  border: `1px solid ${css('border')}`,
  borderRadius: 4,
  background: 'none',
  color: 'inherit',
  font: 'inherit',
  cursor: 'pointer',
};

function CellView({
  cell,
  format,
  bands,
  colors,
  staleAfter,
  ageLag,
  newest,
  now,
  strings,
  wrap,
}: {
  cell: Cell;
  format: ColumnFormat | undefined;
  bands: Threshold[] | undefined;
  colors: ReturnType<typeof useSeverityColors>;
  staleAfter: number | undefined;
  // Show the age only past this lag behind `newest` (seconds), or when stale; undefined = always.
  ageLag: number | undefined;
  newest: number | undefined;
  now: number;
  strings: FleetStrings;
  // Let the text break (the narrow card) instead of holding one line (the table cell).
  wrap?: boolean;
}) {
  const muted: CSSProperties = { color: css('muted-foreground'), fontSize: fontVar('sm'), ...(wrap ? { overflowWrap: 'anywhere' as const } : {}) };
  switch (cell.kind) {
    case 'loading':
      return <span style={muted} title={strings.loading} aria-label={strings.loading}>…</span>;
    case 'failed':
      return <span style={muted}>{strings.failed}</span>;
    case 'not-reported':
      return <span style={muted} data-cell-empty="not-reported">{strings.notReported}</span>;
    case 'unread':
      return <span style={muted} data-cell-empty="unread">{strings.unread}</span>;
    case 'no-data':
      return <span style={muted} data-cell-empty="no-data">{strings.noData}</span>;
    case 'value': {
      const band = bands ? bandAt(bands, cell.value) : undefined;
      // The normal band has no colour: the cell keeps the table's own.
      const color = levelColor(colors, band?.level);
      const at = cell.sample.occurredTime ? Date.parse(cell.sample.occurredTime) : Number.NaN;
      const age = Number.isNaN(at) ? undefined : ageSeconds(at, now);
      const stale = age !== undefined && isStale(age, staleAfter);
      const showAge = age !== undefined && (ageLag === undefined || stale || newest === undefined || newest - at >= ageLag * 1000);
      return (
        <span data-stale={stale ? '' : undefined} style={{ display: 'inline-flex', flexDirection: 'column', opacity: stale ? 0.6 : 1 }}>
          <span data-threshold-level={band?.level} style={{ fontVariantNumeric: 'tabular-nums', color }}>
            {formatValue(cell.value, format?.precision)}
            {format?.unit ? ` ${format.unit}` : ''}
          </span>
          {age !== undefined && showAge && (
            <span style={muted} data-cell-age="">
              {formatAge(age)}
              {stale ? ` · ${strings.stale}` : ''}
            </span>
          )}
        </span>
      );
    }
  }
}
