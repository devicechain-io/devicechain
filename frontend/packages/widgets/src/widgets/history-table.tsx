// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// history-table -- one device's stored history as rows: a row per time bucket over the board's span,
// a column per measurement, each cell the AVERAGE of that measurement's readings in the bucket. It
// answers "give me the numbers" without leaving the board, and its frame menu exports them as CSV.
//
// 🔴 A BUCKET AVERAGE IS NOT A READING. The table says so wherever a figure could be taken for one:
// the footer reads "avg per 5m" (with the sentence in its title), the time is the START of the
// bucket, and the export's headings and file name carry the same words. There is no live tail: the
// newest row is a bucket that is still filling, and the table reads it again on the bucket's
// boundaries (history-feed.tsx).
//
//   - a cell with no number is "—" with a reason: that measurement has no readings in that bucket;
//   - newest first unless `oldestFirst`; long histories are paged (`pageSize`), and the export holds
//     every row, not the page on screen;
//   - on a narrow board the table becomes a card per bucket, so nothing is cut off at the right edge.

import {
  css,
  fontVar,
  formatDateTime,
  formatTimestamp,
  formatValue,
  optBoolean,
  optNumber,
  optString,
  spaceVar,
  useWidgetScale,
  useWidgetTimeZone,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';
import { useMemo, useState, type CSSProperties, type ReactNode } from 'react';

import {
  DEFAULT_PAGE_SIZE,
  bucketLabel,
  clampPage,
  columnHeading,
  historyColumns,
  historyExport,
  historyRows,
  observedNames,
  pageCount,
  spansDays,
  type HistoryColumn,
  type HistoryRow,
  type HistoryView,
} from '../history-model';
import { historyStrings, type HistoryStrings } from '../history-strings';
import { LevelEmpty } from '../level-readout';

// Below this width (px of the widget body) the table becomes a card per bucket.
export const STACK_BELOW_PX = 480;

// The author's column list, read defensively: a hand-edited definition may carry anything, and an
// entry that cannot be read drops out rather than take the table down.
export function authoredColumns(raw: unknown): HistoryColumn[] {
  if (!Array.isArray(raw)) return [];
  const out: HistoryColumn[] = [];
  for (const item of raw) {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) continue;
    const entry = item as Record<string, unknown>;
    if (typeof entry.measurement !== 'string' || entry.measurement === '') continue;
    const column: HistoryColumn = { name: entry.measurement };
    if (typeof entry.label === 'string' && entry.label !== '') column.label = entry.label;
    if (typeof entry.unit === 'string' && entry.unit !== '') column.unit = entry.unit;
    if (typeof entry.precision === 'number' && Number.isInteger(entry.precision) && entry.precision >= 0 && entry.precision <= 100) {
      column.precision = entry.precision;
    }
    out.push(column);
  }
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
// The time column stays in view while the measurement columns scroll under it.
const stickyFirst: CSSProperties = { position: 'sticky', left: 0, background: css('card') };
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

export function HistoryTable({ widget, history }: WidgetProps & { history?: HistoryView }) {
  const strings = historyStrings();
  const title = optString(widget.options, 'title');
  const message = (text: string) => (
    <WidgetFrame title={title} flush>
      <LevelEmpty message={text} />
    </WidgetFrame>
  );
  if (!history) return message(strings.loading);
  if (history.status !== 'ready') return message(strings[history.status]);
  return <Ready widget={widget} history={history} strings={strings} />;
}

function Ready({
  widget,
  history,
  strings,
}: {
  widget: WidgetProps['widget'];
  history: Extract<HistoryView, { status: 'ready' }>;
  strings: HistoryStrings;
}) {
  const timeZone = useWidgetTimeZone();
  const title = optString(widget.options, 'title');
  const precision = optNumber(widget.options, 'precision');
  const oldestFirst = optBoolean(widget.options, 'oldestFirst') === true;
  const pageSize = optNumber(widget.options, 'pageSize') ?? DEFAULT_PAGE_SIZE;
  const authored = useMemo(() => authoredColumns(widget.options?.columns), [widget.options?.columns]);
  const columns = useMemo(
    () => historyColumns(authored, widget.datasource?.measurements ?? [], observedNames(history.buckets)),
    [authored, widget.datasource, history.buckets],
  );
  const rows = useMemo(() => historyRows(history.buckets, columns.map((c) => c.name), oldestFirst), [history.buckets, columns, oldestFirst]);
  const [page, setPage] = useState(0);
  const shown = clampPage(page, rows.length, pageSize);
  const pages = pageCount(rows.length, pageSize);
  const pageRows = rows.slice(shown * pageSize, (shown + 1) * pageSize);
  const bucket = bucketLabel(history.window.intervalSeconds);
  const dated = spansDays(rows, timeZone);
  const timeOf = (row: HistoryRow): string => (dated ? formatDateTime(row.bucketStart, timeZone) : formatTimestamp(row.bucketStart, timeZone));

  const subject = [title ?? 'history', widget.datasource?.kind === 'device' ? widget.datasource.deviceToken : undefined].filter(Boolean).join('-');
  // Built when the viewer picks the export, from the rows the table holds now: every row, in the order
  // on screen, over the window these buckets were read for.
  const exportRows = () =>
    historyExport({
      subject,
      columns,
      rows,
      window: history.window,
      precision,
      heading: { time: strings.exportTime, average: strings.average },
    });

  const cell = (row: HistoryRow, c: HistoryColumn): ReactNode => {
    const v = row.averages.get(c.name);
    if (v === undefined) {
      return (
        <span style={{ color: css('muted-foreground') }} title={strings.noReadings} data-cell-empty="">
          —
        </span>
      );
    }
    return <span style={{ fontVariantNumeric: 'tabular-nums' }}>{formatValue(v, c.precision ?? precision)}</span>;
  };

  const wide = (
    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: fontVar('md'), color: css('foreground') }}>
      <thead>
        <tr>
          <th scope="col" style={{ ...headBox, ...stickyFirst, zIndex: 2 }}>
            {strings.time}
          </th>
          {columns.map((c) => (
            <th key={c.name} scope="col" style={headBox}>
              {columnHeading(c)}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {pageRows.map((row) => (
          <tr key={row.bucketStart} data-bucket={row.bucketStart} style={{ borderBottom: `1px solid ${css('border')}` }}>
            <th scope="row" style={{ ...cellBox, ...stickyFirst, fontWeight: 500, fontVariantNumeric: 'tabular-nums' }}>
              {timeOf(row)}
            </th>
            {columns.map((c) => (
              <td key={c.name} data-measurement={c.name} style={cellBox}>
                {cell(row, c)}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );

  const narrow = (
    <div data-history-stacked="" style={{ color: css('foreground'), fontSize: fontVar('md') }}>
      {pageRows.map((row) => (
        <div key={row.bucketStart} data-bucket={row.bucketStart} style={{ padding: `${spaceVar(2)} ${spaceVar(3)}`, borderTop: `1px solid ${css('border')}` }}>
          <div style={{ fontWeight: 500, overflowWrap: 'anywhere' }}>{timeOf(row)}</div>
          {columns.map((c) => (
            <div key={c.name} data-measurement={c.name} style={{ display: 'flex', justifyContent: 'space-between', gap: spaceVar(3), alignItems: 'baseline' }}>
              <span style={{ color: css('muted-foreground'), fontSize: fontVar('sm'), overflowWrap: 'anywhere' }}>{columnHeading(c)}</span>
              <span style={{ textAlign: 'right', overflowWrap: 'anywhere' }}>{cell(row, c)}</span>
            </div>
          ))}
        </div>
      ))}
    </div>
  );

  return (
    <WidgetFrame title={title} flush exportRows={exportRows}>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
        {rows.length === 0 ? (
          <LevelEmpty message={strings.empty} />
        ) : (
          // A focusable scroll region: a keyboard user can scroll a wide table sideways.
          <div role="region" aria-label={title ?? strings.time} tabIndex={0} data-history-scroll="" style={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
            <Responsive wide={wide} narrow={narrow} />
          </div>
        )}
        <Footer
          strings={strings}
          bucket={bucket}
          oldestFirst={oldestFirst}
          pages={pages}
          shown={shown}
          from={shown * pageSize + 1}
          to={shown * pageSize + pageRows.length}
          of={rows.length}
          onPage={setPage}
        />
      </div>
    </WidgetFrame>
  );
}

function Responsive({ wide, narrow }: { wide: ReactNode; narrow: ReactNode }) {
  const { width } = useWidgetScale().size;
  // An unmeasured body (width 0: before the first layout, or under a test renderer) takes the table.
  return width > 0 && width < STACK_BELOW_PX ? narrow : wide;
}

// Below this width (px of the widget body) the footer keeps only what cannot be left out: the basis of
// the figures and the page buttons. The rest moves to their titles, so a table at the smallest span
// still has a footer that fits on one line.
const COMPACT_FOOTER_BELOW_PX = 260;

function Footer({
  strings,
  bucket,
  oldestFirst,
  pages,
  shown,
  from,
  to,
  of,
  onPage,
}: {
  strings: HistoryStrings;
  bucket: string;
  oldestFirst: boolean;
  pages: number;
  shown: number;
  from: number;
  to: number;
  of: number;
  onPage: (page: number) => void;
}) {
  const { width } = useWidgetScale().size;
  const compact = width > 0 && width < COMPACT_FOOTER_BELOW_PX;
  const order = oldestFirst ? strings.oldestFirst : strings.newestFirst;
  const range = strings.range(from, to, of);
  return (
    <div
      data-history-footer=""
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
      <span data-history-bucket={bucket} title={`${strings.averageTitle(bucket)} (${order})`}>
        {compact ? strings.average(bucket) : `${strings.average(bucket)} · ${order}`}
      </span>
      {pages > 1 && (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: spaceVar(2) }} title={compact ? range : undefined}>
          <button type="button" style={pagerButton} disabled={shown === 0} onClick={() => onPage(shown - 1)} aria-label={strings.previous}>
            ‹
          </button>
          {compact ? null : <span>{range}</span>}
          <button type="button" style={pagerButton} disabled={shown >= pages - 1} onClick={() => onPage(shown + 1)} aria-label={strings.next}>
            ›
          </button>
        </span>
      )}
    </div>
  );
}
