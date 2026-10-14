// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The shared card frame every widget renders inside — a bordered, themed surface
// that fills its dashboard-canvas slot, with an optional title bar. Styled with
// inline CSS-variable references so it themes from the host without Tailwind.

import { type CSSProperties, type ReactNode } from 'react';

import { useWidgetFreshness, useWidgetSubject } from './context';
import { FreshnessReadout, StaleBadge, useFreshness } from './freshness-readout';
import { useElementSize } from './hooks';
import { css } from './theme';

// Below either of these (px, content box) the age readout is dropped so a small widget keeps
// its room for the value; a stale widget still shows its badge, in the corner.
const FRESHNESS_MIN_WIDTH = 150;
const FRESHNESS_MIN_HEIGHT = 110;

export interface WidgetFrameProps {
  title?: string;
  // An explicit subject line; when omitted the frame falls back to the resolved-entity
  // subject from WidgetSubjectContext.
  subtitle?: string;
  children: ReactNode;
  // Extra style for the body region (e.g. removing padding for a chart canvas).
  bodyStyle?: CSSProperties;
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

export function WidgetFrame({ title, subtitle, children, bodyStyle, sampleTime }: WidgetFrameProps) {
  const subject = useWidgetSubject();
  // A subtitle that only repeats the title says nothing (a card titled by its device's
  // external id, under a subject line naming the same device).
  const rawSub = subtitle ?? subject;
  const sub = rawSub && rawSub.trim() === title?.trim() ? undefined : rawSub;

  // Present only for a widget whose descriptor opts in. Stale (a known threshold, passed) greys
  // the value out; with no threshold the age is shown and nothing is ever greyed.
  const freshness = useWidgetFreshness();
  const at = freshness === undefined ? undefined : sampleTime === undefined ? freshness.at : parseSampleTime(sampleTime);
  const reading = useFreshness(at, freshness?.staleAfterSeconds);
  const [rootRef, size] = useElementSize<HTMLDivElement>();
  const compact = size.width > 0 && (size.width < FRESHNESS_MIN_WIDTH || size.height < FRESHNESS_MIN_HEIGHT);
  const stale = reading?.stale === true;

  return (
    <div
      ref={rootRef}
      data-stale={stale ? 'true' : undefined}
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
      {title || sub ? (
        <div
          style={{
            padding: '8px 12px',
            borderBottom: `1px solid ${css('border')}`,
          }}
        >
          {title ? (
            <div
              title={title}
              style={{
                fontSize: 12,
                fontWeight: 600,
                color: css('muted-foreground'),
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
              }}
            >
              {title}
            </div>
          ) : null}
          {sub ? (
            <div
              title={sub}
              style={{
                fontSize: 11,
                fontWeight: 400,
                color: css('muted-foreground'),
                opacity: 0.75,
                marginTop: title ? 2 : 0,
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
          flex: 1,
          minHeight: 0,
          minWidth: 0,
          overflow: 'hidden',
          ...bodyStyle,
          ...(stale ? { opacity: 0.45, filter: 'grayscale(1)' } : null),
        }}
      >
        {children}
      </div>
      {reading && !compact ? (
        <div
          data-widget-freshness=""
          style={{ padding: '3px 12px', borderTop: `1px solid ${css('border')}`, flex: '0 0 auto' }}
        >
          <FreshnessReadout reading={reading} />
        </div>
      ) : null}
      {reading && compact && stale ? (
        <div style={{ position: 'absolute', top: 4, right: 4 }}>
          <StaleBadge />
        </div>
      ) : null}
    </div>
  );
}
