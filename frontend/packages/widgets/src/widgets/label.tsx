// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// label — static text on the canvas (headings, captions, annotations). No
// datasource. Renders plain text only (no raw HTML) so a tenant-authored
// definition can't inject markup.

import type { CSSProperties } from 'react';

import {
  css,
  defineWidget,
  fontVar,
  spaceVar,
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
  // An authored size is a design size in px at the board's normal density: it scales with it,
  // so a wall board's label grows with the rest. Unset takes the large step of the type ramp.
  const authored = optNumber(widget.options, 'fontSize');
  const fontSize = authored !== undefined ? `calc(${authored}px * var(--dc-scale, 1))` : fontVar('lg');

  return (
    <WidgetFrame title={optString(widget.options, 'title')} flush>
      <div
        style={{
          display: 'flex',
          alignItems: 'safe center',
          justifyContent: justify[align] ?? 'center',
          height: '100%',
          boxSizing: 'border-box',
          overflow: 'hidden',
          // A label is often one row tall: the frame's uniform inset would leave it a line's
          // height or less, so it keeps a slim vertical inset and the usual side inset.
          padding: `${spaceVar(1)} ${spaceVar(3)}`,
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
