// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// label — static text on the canvas (headings, captions, annotations). No
// datasource. Renders plain text only (no raw HTML) so a tenant-authored
// definition can't inject markup.

import type { CSSProperties } from 'react';

import { WidgetFrame } from '../frame';
import { css } from '../theme';
import { optNumber, optString, type WidgetProps } from '../widget';

const justify: Record<string, CSSProperties['justifyContent']> = {
  left: 'flex-start',
  center: 'center',
  right: 'flex-end',
};

export function Label({ widget }: WidgetProps) {
  const text = optString(widget.options, 'text') ?? '';
  const align = optString(widget.options, 'align') ?? 'center';
  const fontSize = optNumber(widget.options, 'fontSize') ?? 16;

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: justify[align] ?? 'center',
          height: '100%',
          padding: 12,
        }}
      >
        <span
          style={{
            fontSize,
            color: css('foreground'),
            textAlign: align as CSSProperties['textAlign'],
            whiteSpace: 'pre-wrap',
          }}
        >
          {text}
        </span>
      </div>
    </WidgetFrame>
  );
}
