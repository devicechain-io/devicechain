// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The shared card frame every widget renders inside — a bordered, themed surface
// that fills its dashboard-canvas slot, with an optional title bar. Styled with
// inline CSS-variable references so it themes from the host without Tailwind.
//
// The frame is SIZE-AWARE. Its content column -- the padded box the widget draws in -- is a
// `container-type: size` query container, so HTML text can size itself with `cq` units on the
// first frame, and the SAME box is measured (quantised to 4 px) and handed to everything inside
// through context (useWidgetScale), so a canvas widget fits to the identical numbers. The header
// gives way to the body as the widget shrinks: the subtitle goes first, then the title.

import { sameLabel } from '@devicechain/dashboards';
import { useMemo, type CSSProperties, type ReactNode } from 'react';

import { useWidgetDensityScale, useWidgetFreshness, useWidgetSubject, WidgetBodySizeProvider } from './context';
import { FreshnessReadout, StaleBadge, useFreshness } from './freshness-readout';
import { useElementSize } from './hooks';
import { css } from './theme';
import { makeWidgetScale, type WidgetScale } from './scale';
import { fontVar, spaceVar } from './tokens';

// Below either of these (px, content box) the age readout is dropped so a small widget keeps
// its room for the value; a stale widget still shows its badge, in the corner.
const FRESHNESS_MIN_WIDTH = 150;
const FRESHNESS_MIN_HEIGHT = 110;

// Frame heights (px at density 1, multiplied by the density scale) below which the header gives
// way. The subtitle is the first thing to go; the title holds until there is hardly room for a
// value at all. A dropped title stays available to assistive tech as the frame's aria-label.
export const FRAME_SUBTITLE_MIN_HEIGHT = 120;
export const FRAME_TITLE_MIN_HEIGHT = 76;

// The content column's size is quantised to this many px before it reaches a widget.
const BODY_SIZE_STEP = 4;

export interface WidgetFrameProps {
  title?: string;
  // An explicit subject line; when omitted the frame falls back to the resolved-entity
  // subject from WidgetSubjectContext.
  subtitle?: string;
  // The widget's content. A function child receives the frame's measured scale (size class,
  // content-column size, token px), for a widget whose own render needs it: a component
  // rendered INSIDE the frame can call useWidgetScale() instead.
  children: ReactNode | ((scale: WidgetScale) => ReactNode);
  // Extra style for the body region.
  bodyStyle?: CSSProperties;
  // Remove the content column's padding, for a widget that fills its box edge to edge (a chart
  // canvas, a map, an image). The column is still the measured, queryable box.
  flush?: boolean;
  // The occurredTime of the ONE sample this widget is showing, for a widget that shows a single
  // value (null while it has none). When set, the age readout ages exactly that sample; left
  // undefined, it ages the newest sample the host holds for the widget. A single-value widget
  // must pass it: on a device reporting several measurements, the newest is often not the one
  // on screen, and the age of another measurement would make a frozen value look live.
  sampleTime?: string | null;
}

// The epoch-ms time of a sample timestamp, or undefined when it has none or is unparseable.
function parseSampleTime(time: string | null): number | undefined {
  if (!time) return undefined;
  const at = Date.parse(time);
  return Number.isNaN(at) ? undefined : at;
}

export function WidgetFrame({ title, subtitle, children, bodyStyle, flush, sampleTime }: WidgetFrameProps) {
  const subject = useWidgetSubject();
  // A subtitle that only repeats the title says nothing (a card titled by its device's
  // external id, under a subject line naming the same device).
  const rawSub = subtitle ?? subject;
  const dupSub = rawSub && sameLabel(rawSub, title) ? undefined : rawSub;

  // Present only for a widget whose descriptor opts in. Stale (a known threshold, passed) greys
  // the value out; with no threshold the age is shown and nothing is ever greyed.
  const freshness = useWidgetFreshness();
  const at = freshness === undefined ? undefined : sampleTime === undefined ? freshness.at : parseSampleTime(sampleTime);
  const reading = useFreshness(at, freshness?.staleAfterSeconds);
  const [rootRef, size] = useElementSize<HTMLDivElement>();
  const [columnRef, columnSize] = useElementSize<HTMLDivElement>(BODY_SIZE_STEP);
  const scale = useWidgetDensityScale();
  // Unmeasured (first frame, jsdom) shows everything; the cq path needs no measure to be right.
  const measured = size.height > 0;
  const showSub = !measured || size.height >= FRAME_SUBTITLE_MIN_HEIGHT * scale;
  const showTitle = !measured || size.height >= FRAME_TITLE_MIN_HEIGHT * scale;
  const sub = showSub ? dupSub : undefined;
  const shownTitle = showTitle ? title : undefined;
  const widgetScale = useMemo(() => makeWidgetScale(columnSize, scale), [columnSize, scale]);
  const compact = size.width > 0 && (size.width < FRESHNESS_MIN_WIDTH || size.height < FRESHNESS_MIN_HEIGHT);
  const stale = reading?.stale === true;

  return (
    <div
      ref={rootRef}
      data-stale={stale ? 'true' : undefined}
      role={title && !showTitle ? 'group' : undefined}
      aria-label={title && !showTitle ? title : undefined}
      style={{
        position: 'relative',
        display: 'flex',
        flexDirection: 'column',
        width: '100%',
        height: '100%',
        boxSizing: 'border-box',
        background: css('card'),
        color: css('card-foreground'),
        border: `1px solid ${css('border')}`,
        borderRadius: 'var(--radius, 8px)',
        overflow: 'hidden',
      }}
    >
      {shownTitle || sub ? (
        <div
          style={{
            padding: `${spaceVar(2)} ${spaceVar(3)}`,
            borderBottom: `1px solid ${css('border')}`,
          }}
        >
          {shownTitle ? (
            <div
              title={shownTitle}
              style={{
                fontSize: fontVar('sm'),
                fontWeight: 600,
                color: css('muted-foreground'),
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
              }}
            >
              {shownTitle}
            </div>
          ) : null}
          {sub ? (
            <div
              title={sub}
              style={{
                fontSize: fontVar('xs'),
                fontWeight: 400,
                color: css('muted-foreground'),
                opacity: 0.75,
                marginTop: shownTitle ? 2 : 0,
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
              }}
            >
              {sub}
            </div>
          ) : null}
        </div>
      ) : null}
      {/* The body clips: the card root already stops a widget painting outside the card, and
          this stops one that outgrows its slot painting up over the title bar. minWidth 0
          lets it shrink inside the flex column. */}
      <div
        data-widget-body=""
        style={{
          position: 'relative',
          flex: 1,
          minHeight: 0,
          minWidth: 0,
          overflow: 'hidden',
          ...bodyStyle,
          ...(stale ? { opacity: 0.45, filter: 'grayscale(1)' } : null),
        }}
      >
        {/* The content column: absolutely filling the body so its size never depends on its
            children (a size container must not), padded by the frame so every widget gets the
            same inset, and the box both the cq units and the measured size describe. */}
        <div
          ref={columnRef}
          data-widget-content=""
          style={{
            position: 'absolute',
            inset: 0,
            boxSizing: 'border-box',
            containerType: 'size',
            overflow: 'hidden',
            padding: flush ? 0 : spaceVar(3),
          }}
        >
          <WidgetBodySizeProvider size={columnSize}>
            {typeof children === 'function' ? children(widgetScale) : children}
          </WidgetBodySizeProvider>
        </div>
      </div>
      {reading && !compact ? (
        <div
          data-widget-freshness=""
          style={{ padding: `${spaceVar(1)} ${spaceVar(3)}`, borderTop: `1px solid ${css('border')}`, flex: '0 0 auto' }}
        >
          <FreshnessReadout reading={reading} />
        </div>
      ) : null}
      {reading && compact && stale ? (
        <div style={{ position: 'absolute', top: spaceVar(1), right: spaceVar(1) }}>
          <StaleBadge />
        </div>
      ) : null}
    </div>
  );
}
