// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A small header badge for a hub's connection state. Presentational: the host supplies the
// words (its own i18n), so the SDK adds no strings of its own here.

import type { HubConnectionState } from '@devicechain/dashboards';

import { css, type ThemeVar } from './theme';

export interface HubConnectionBadgeProps {
  state: HubConnectionState;
  labels: Record<HubConnectionState, string>;
}

const DOT: Record<HubConnectionState, ThemeVar> = {
  live: 'primary',
  reconnecting: 'muted-foreground',
  offline: 'destructive',
};

export function HubConnectionBadge({ state, labels }: HubConnectionBadgeProps) {
  return (
    <span
      role="status"
      data-connection-state={state}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        fontSize: 12,
        padding: '2px 8px',
        borderRadius: 999,
        border: `1px solid ${css('border')}`,
        color: state === 'offline' ? css('destructive') : css('muted-foreground'),
        whiteSpace: 'nowrap',
      }}
    >
      <span
        aria-hidden
        style={{
          width: 8,
          height: 8,
          borderRadius: '50%',
          background: css(DOT[state]),
          opacity: state === 'reconnecting' ? 0.6 : 1,
        }}
      />
      {labels[state]}
    </span>
  );
}
