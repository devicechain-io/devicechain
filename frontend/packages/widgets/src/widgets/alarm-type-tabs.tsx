// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tab strip of an alarm table grouped by device type: "All", then one tab per type, each with an
// attention badge. A small accessible tablist built here because the widget packages carry no UI kit.
//
// Roving tabindex, MANUAL activation: the arrow keys (and Home/End) move focus along the strip and
// Enter or Space chooses the tab, because choosing one reads that tab's rows and walking past five
// tabs should not read five tabs' worth of alarms. The strip scrolls sideways on a narrow board
// rather than wrapping.
//
// The badge follows the platform rule that colour is for the abnormal: it appears only for a tab
// with an ACTIVE alarm, its dot takes the colour of the worst severity, and an INDETERMINATE worst
// (no alarm hue) stays neutral. The count and the worst severity are also text for assistive tech,
// so the signal never rides on hue alone.

import type { AlarmTypeTab } from '@devicechain/dashboards';
import { css, fontVar, useSeverityColors } from '@devicechain/widget-sdk';
import { useRef, type CSSProperties, type KeyboardEvent } from 'react';

import { alarmStrings, type AlarmStrings } from '../alarm-strings';
import { severityColor } from './severity';

export interface AlarmTypeTabsProps {
  // Every tab to show, All excluded.
  tabs: readonly AlarmTypeTab[];
  selected: string | null;
  onSelect: (token: string | null) => void;
  truncated: boolean;
  // Ids the strip and its panel share, so each tab names the panel and the panel names its tab.
  idBase: string;
}

const tabStyle = (active: boolean): CSSProperties => ({
  flex: '0 0 auto',
  display: 'inline-flex',
  alignItems: 'center',
  gap: 6,
  padding: '6px 10px',
  border: 'none',
  borderBottom: `2px solid ${active ? css('primary') : 'transparent'}`,
  background: 'none',
  font: 'inherit',
  fontSize: fontVar('md'),
  fontWeight: active ? 600 : 400,
  color: active ? css('foreground') : css('muted-foreground'),
  cursor: 'pointer',
  whiteSpace: 'nowrap',
});

export const tabDomId = (idBase: string, token: string | null): string => `${idBase}-tab-${token ?? ''}`;
export const panelDomId = (idBase: string): string => `${idBase}-panel`;

export function AlarmTypeTabsStrip({ tabs, selected, onSelect, truncated, idBase }: AlarmTypeTabsProps) {
  const strings = alarmStrings();
  const severity = useSeverityColors();
  const refs = useRef(new Map<string | null, HTMLButtonElement>());
  const order: Array<string | null> = [null, ...tabs.map((t) => t.token)];
  // A selection the strip does not hold (a pinned list that dropped it) reads as All.
  const current = order.includes(selected) ? selected : null;

  const move = (event: KeyboardEvent, from: number): void => {
    const to =
      event.key === 'ArrowRight' ? (from + 1) % order.length
      : event.key === 'ArrowLeft' ? (from - 1 + order.length) % order.length
      : event.key === 'Home' ? 0
      : event.key === 'End' ? order.length - 1
      : -1;
    if (to < 0) return;
    event.preventDefault();
    refs.current.get(order[to])?.focus();
  };

  return (
    <div
      role="tablist"
      aria-label={strings.tabsLabel}
      style={{
        display: 'flex',
        flexShrink: 0,
        overflowX: 'auto',
        overflowY: 'hidden',
        borderBottom: `1px solid ${css('border')}`,
      }}
    >
      {order.map((token, i) => {
        const tab = token === null ? undefined : tabs[i - 1];
        const active = token === current;
        return (
          <button
            key={token ?? '\u0000all'}
            ref={(el) => {
              if (el) refs.current.set(token, el);
              else refs.current.delete(token);
            }}
            type="button"
            role="tab"
            id={tabDomId(idBase, token)}
            aria-selected={active}
            aria-controls={panelDomId(idBase)}
            tabIndex={active ? 0 : -1}
            onClick={() => onSelect(token)}
            onKeyDown={(e) => move(e, i)}
            style={tabStyle(active)}
          >
            {token === null ? strings.all : (tab?.name ?? token)}
            {tab && tab.active > 0 ? (
              <Badge tab={tab} truncated={truncated} colors={severity} strings={strings} />
            ) : null}
          </button>
        );
      })}
    </div>
  );
}

function Badge({
  tab,
  truncated,
  colors,
  strings,
}: {
  tab: AlarmTypeTab;
  truncated: boolean;
  colors: ReturnType<typeof useSeverityColors>;
  strings: AlarmStrings;
}) {
  const worst = tab.worstSeverity ?? 'INDETERMINATE';
  return (
    <span
      data-testid="alarm-tab-badge"
      role="img"
      aria-label={strings.activeBadge(tab.active, strings.severity[worst] ?? worst)}
      title={truncated ? strings.countsTruncated : undefined}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 4,
        fontSize: fontVar('xs'),
        fontWeight: 600,
        fontVariantNumeric: 'tabular-nums',
        color: css('foreground'),
      }}
    >
      <span
        data-testid="alarm-tab-badge-dot"
        aria-hidden="true"
        style={{ width: 8, height: 8, borderRadius: '50%', background: severityColor(colors, worst), flexShrink: 0 }}
      />
      <span aria-hidden="true">
        {tab.active}
        {truncated ? '+' : ''}
      </span>
    </span>
  );
}
