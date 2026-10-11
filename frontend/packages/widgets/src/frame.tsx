// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The shared card frame every widget renders inside — a bordered, themed surface
// that fills its dashboard-canvas slot, with an optional title bar. Styled with
// inline CSS-variable references so it themes from the host without Tailwind.

import { type CSSProperties, type ReactNode } from 'react';

import { useWidgetSubject } from '@devicechain/widget-sdk';

import { css } from './theme';

// The ambient host contexts live in @devicechain/widget-sdk — the one place they may be
// defined (see its context module) — and are re-exported here so every import site keeps
// resolving.
export {
  WidgetCandidatesProvider,
  WidgetSelectProvider,
  WidgetSubjectProvider,
  WidgetThemeRootProvider,
  WidgetTimeZoneProvider,
  useWidgetCandidates,
  useWidgetSelect,
  useWidgetSubject,
  useWidgetThemeRoot,
  useWidgetTimeZone,
  type WidgetCandidates,
  type WidgetSelect,
} from '@devicechain/widget-sdk';

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
  const sub = subtitle ?? subject;
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
            <div style={{ fontSize: 12, fontWeight: 600, color: css('muted-foreground') }}>{title}</div>
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
      <div style={{ flex: 1, minHeight: 0, ...bodyStyle }}>{children}</div>
    </div>
  );
}
