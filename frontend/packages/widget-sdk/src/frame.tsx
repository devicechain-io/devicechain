// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The shared card frame every widget renders inside — a bordered, themed surface
// that fills its dashboard-canvas slot, with an optional title bar. Styled with
// inline CSS-variable references so it themes from the host without Tailwind.

import { type CSSProperties, type ReactNode } from 'react';

import { useWidgetSubject } from './context';
import { css } from './theme';

export interface WidgetFrameProps {
  title?: string;
  // An explicit subject line; when omitted the frame falls back to the resolved-entity
  // subject from WidgetSubjectContext.
  subtitle?: string;
  children: ReactNode;
  // Extra style for the body region (e.g. removing padding for a chart canvas).
  bodyStyle?: CSSProperties;
}

export function WidgetFrame({ title, subtitle, children, bodyStyle }: WidgetFrameProps) {
  const subject = useWidgetSubject();
  // A subtitle that only repeats the title says nothing (a card titled by its device's
  // external id, under a subject line naming the same device).
  const rawSub = subtitle ?? subject;
  const sub = rawSub && rawSub.trim() === title?.trim() ? undefined : rawSub;
  return (
    <div
      style={{
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
      <div data-widget-body="" style={{ flex: 1, minHeight: 0, minWidth: 0, overflow: 'hidden', ...bodyStyle }}>
        {children}
      </div>
    </div>
  );
}
