// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The shared card frame every widget renders inside — a bordered, themed surface
// that fills its dashboard-canvas slot, with an optional title bar. Styled with
// inline CSS-variable references so it themes from the host without Tailwind.

import type { CSSProperties, ReactNode } from 'react';

import { css } from './theme';

export interface WidgetFrameProps {
  title?: string;
  children: ReactNode;
  // Extra style for the body region (e.g. removing padding for a chart canvas).
  bodyStyle?: CSSProperties;
}

export function WidgetFrame({ title, children, bodyStyle }: WidgetFrameProps) {
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
      {title ? (
        <div
          style={{
            padding: '8px 12px',
            fontSize: 12,
            fontWeight: 600,
            color: css('muted-foreground'),
            borderBottom: `1px solid ${css('border')}`,
          }}
        >
          {title}
        </div>
      ) : null}
      <div style={{ flex: 1, minHeight: 0, ...bodyStyle }}>{children}</div>
    </div>
  );
}
