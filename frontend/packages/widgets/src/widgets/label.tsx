// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// label — static text on the canvas (headings, captions, annotations). No
// datasource. Renders plain text only (no raw HTML) so a tenant-authored
// definition can't inject markup.

import type { CSSProperties } from 'react';

import {
  css,
  defineWidget,
  optNumber,
  optString,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';

const justify: Record<string, CSSProperties['justifyContent']> = {
  left: 'flex-start',
  center: 'center',
  right: 'flex-end',
};

export function Label({ widget }: WidgetProps<null>) {
  const text = optString(widget.options, 'text') ?? '';
  const align = optString(widget.options, 'align') ?? 'center';
  const fontSize = optNumber(widget.options, 'fontSize') ?? 16;

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div
        style={{
          display: 'flex',
          alignItems: 'safe center',
          justifyContent: justify[align] ?? 'center',
          height: '100%',
          boxSizing: 'border-box',
          overflow: 'hidden',
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

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const labelWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['label'],
  implementation: { kind: 'trusted-react', component: Label },
});
