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
//
// What the card LOOKS like is the board's to decide (the frame a definition sets on its canvas
// and per widget, resolved by the renderer and read here through useWidgetFrameStyle): a card, a
// borderless flat panel, or nothing at all; its border width, radius and colour; its padding;
// its background; and whether the header shows. Every value is a token. A widget mounted outside
// a board gets the card.

import { resolveFrame, sameLabel, type FrameRadius, type ResolvedFrame, type SpaceTokenName } from '@devicechain/dashboards';
import { useCallback, useEffect, useMemo, useState, type ComponentType, type CSSProperties, type ReactNode } from 'react';

import {
  WidgetImageRegistrarProvider,
  useWidgetDensityScale,
  useWidgetFrameStyle,
  useWidgetFreshness,
  useWidgetSubject,
  WidgetBodySizeProvider,
} from './context';
import type { ExportButtonProps } from './export-button';
import { FreshnessReadout, StaleBadge, useFreshness } from './freshness-readout';
import { useElementSize } from './hooks';
import { css } from './theme';
import { makeWidgetScale, type WidgetScale } from './scale';
import { fontVar, spaceVar, type SpaceToken } from './tokens';
import type { WidgetExportResult } from './widget-export';
import type { WidgetImageSource } from './context';

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

// The export button's side (px); the title bar reserves it beside the title.
const EXPORT_BUTTON_SIZE = 24;

// The card a widget gets when no board says otherwise.
const DEFAULT_FRAME: ResolvedFrame = resolveFrame(undefined, undefined);

const RADIUS: Record<FrameRadius, string> = {
  none: '0px',
  sm: 'calc(var(--radius, 8px) / 2)',
  md: 'var(--radius, 8px)',
  lg: 'calc(var(--radius, 8px) * 1.5)',
};

function spaceTokenVar(token: SpaceTokenName): string {
  return token === 'space-0' ? '0px' : spaceVar(Number(token.slice('space-'.length)) as SpaceToken);
}

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
  // Makes the data the widget shows downloadable: the frame draws an export button in its title bar,
  // and "Download CSV" calls this when the viewer picks it, so a table is built only on demand, from
  // what the widget is showing at that moment (the span and the bucket on screen, not a new
  // question). Return nothing to export nothing. OPTIONAL and feature-detected: a widget that passes
  // none gets no CSV item (a widget drawing an <EChart> still gets "Download image"), and a frame
  // whose title bar is hidden (a board that hides headers, or a very short widget) draws no button.
  exportRows?: () => WidgetExportResult;
}

// The epoch-ms time of a sample timestamp, or undefined when it has none or is unparseable.
function parseSampleTime(time: string | null): number | undefined {
  if (!time) return undefined;
  const at = Date.parse(time);
  return Number.isNaN(at) ? undefined : at;
}

export function WidgetFrame({ title, subtitle, children, bodyStyle, flush, sampleTime, exportRows }: WidgetFrameProps) {
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
  // A chart inside registers a way to read its image (see WidgetImageRegistrarProvider); the frame
  // offers the export menu while there is a table to download or an image to save.
  const [image, setImage] = useState<{ source: WidgetImageSource }>();
  const registerImage = useCallback((source: WidgetImageSource | undefined) => setImage(source ? { source } : undefined), []);
  const [columnRef, columnSize] = useElementSize<HTMLDivElement>(BODY_SIZE_STEP);
  const scale = useWidgetDensityScale();
  const frame = useWidgetFrameStyle() ?? DEFAULT_FRAME;
  // Unmeasured (first frame, jsdom) shows everything; the cq path needs no measure to be right.
  // `show` keeps the header whatever the size, `hide` drops it whatever the size.
  const measured = size.height > 0;
  const header = frame.header;
  const showSub = header === 'show' || (header === 'auto' && (!measured || size.height >= FRAME_SUBTITLE_MIN_HEIGHT * scale));
  const showTitle = header === 'show' || (header === 'auto' && (!measured || size.height >= FRAME_TITLE_MIN_HEIGHT * scale));
  const rule = frame.border.width > 0 ? `${frame.border.width}px solid ${css(frame.border.color)}` : 'none';
  const sub = showSub ? dupSub : undefined;
  const shownTitle = showTitle ? title : undefined;
  const widgetScale = useMemo(() => makeWidgetScale(columnSize, scale), [columnSize, scale]);
  // The export button lives in the title bar, so it goes with the title bar.
  const exportable = Boolean(shownTitle || sub) && (exportRows !== undefined || image !== undefined);
  // The button is its own chunk, fetched once a frame has something to export; until it arrives the
  // title bar already reserves its room, so nothing moves when it does.
  const [ExportButton, setExportButton] = useState<{ component: ComponentType<ExportButtonProps> }>();
  useEffect(() => {
    if (!exportable || ExportButton) return;
    let live = true;
    import('./export-button').then(
      (m) => live && setExportButton({ component: m.ExportButton }),
      // No button is a missing convenience, not a broken widget; the next mount tries again.
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [exportable, ExportButton]);
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
        background: frame.background === 'transparent' ? 'transparent' : css(frame.background),
        color: css('card-foreground'),
        border: rule,
        borderRadius: RADIUS[frame.border.radius],
        overflow: 'hidden',
      }}
    >
      {shownTitle || sub ? (
        <div
          style={{
            position: 'relative',
            padding: `${spaceVar(2)} ${spaceVar(3)}`,
            // Room for the export button, which sits at the bar's right edge.
            ...(exportable ? { paddingRight: `calc(${spaceVar(3)} + ${EXPORT_BUTTON_SIZE}px)` } : null),
            borderBottom: rule,
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
          {exportable && ExportButton ? (
            <ExportButton.component size={EXPORT_BUTTON_SIZE} exportRows={exportRows} image={image?.source} title={title} frameElement={() => rootRef.current} />
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
            padding: flush ? 0 : spaceTokenVar(frame.padding),
          }}
        >
          <WidgetBodySizeProvider size={columnSize}>
            <WidgetImageRegistrarProvider register={registerImage}>
              {typeof children === 'function' ? children(widgetScale) : children}
            </WidgetImageRegistrarProvider>
          </WidgetBodySizeProvider>
        </div>
      </div>
      {reading && !compact ? (
        <div
          data-widget-freshness=""
          style={{ padding: `${spaceVar(1)} ${spaceVar(3)}`, borderTop: rule, flex: '0 0 auto' }}
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
