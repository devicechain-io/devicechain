// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a host shows INSTEAD of the board when a deep link could not be honoured (see
// useBoundDevices). One presentational component for every host, so the console and the /dash
// viewer cannot word a refusal differently or, worse, disagree about whether to show the board.
// It carries no copy: the host builds the lines from its own translations.

import type { BindParamIssue } from '@devicechain/dashboards';
import { css, fontVar } from '@devicechain/widget-sdk';

export interface BoundDevicesNoticeProps {
  title: string;
  // One sentence per problem, already worded by the host.
  lines: string[];
  // The explicit way forward: open the board without the parts of the link that failed.
  dismissLabel: string;
  onDismiss: () => void;
  // Offered only when a check failed (so asking again can change the answer).
  retryLabel?: string;
  onRetry?: () => void;
}

export function BoundDevicesNotice({ title, lines, dismissLabel, onDismiss, retryLabel, onRetry }: BoundDevicesNoticeProps) {
  const button = {
    minHeight: 44,
    padding: '0 16px',
    borderRadius: 8,
    border: `1px solid ${css('border')}`,
    background: css('card'),
    color: css('foreground'),
    fontSize: fontVar('md'),
    cursor: 'pointer',
  } as const;
  return (
    <div
      role="alert"
      data-testid="bound-devices-notice"
      style={{
        boxSizing: 'border-box',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'flex-start',
        gap: 12,
        maxWidth: 560,
        margin: '24px auto',
        padding: 16,
        borderRadius: 8,
        border: `1px solid ${css('border')}`,
        background: css('card'),
        color: css('foreground'),
      }}
    >
      <div style={{ fontSize: fontVar('lg'), fontWeight: 600 }}>{title}</div>
      <ul style={{ margin: 0, paddingLeft: 20, display: 'flex', flexDirection: 'column', gap: 4, fontSize: fontVar('md') }}>
        {lines.map((line, i) => (
          <li key={i} style={{ overflowWrap: 'anywhere' }}>
            {line}
          </li>
        ))}
      </ul>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
        {retryLabel && onRetry ? (
          <button type="button" style={button} onClick={onRetry} data-testid="bound-devices-retry">
            {retryLabel}
          </button>
        ) : null}
        <button type="button" style={button} onClick={onDismiss} data-testid="bound-devices-dismiss">
          {dismissLabel}
        </button>
      </div>
    </div>
  );
}

// The lines a refused link reports, in the order a viewer fixes them: what the link got wrong,
// then which devices are not theirs to see, then which could not be checked. The wording is the
// host's; this only decides WHICH problems exist and how many sentences they make.
export interface BoundDevicesWording {
  unknownSlot: (slot: string) => string;
  notADeviceSlot: (slot: string) => string;
  invalidToken: (slot: string, token: string) => string;
  // The link names the slot twice.
  duplicateSlot: (slot: string) => string;
  // The slot's device follows another slot, so a link cannot choose it.
  derivedSlot: (slot: string) => string;
  // "doesn't exist, or you don't have access": one wording, because the server answers one way.
  missing: (slot: string, token: string) => string;
  failed: (slot: string, token: string) => string;
}

// A slot name or a value that failed the grammar is whatever the link's sender typed, and it is
// shown inside the product's own UI: clipped, so a link cannot put a paragraph of its own words
// ("your session expired, sign in at …") in front of the viewer. A valid token is at most 128
// characters of [A-Za-z0-9_-] and is shown whole.
const ECHO_MAX = 48;
const echo = (s: string) => (s.length > ECHO_MAX ? `${s.slice(0, ECHO_MAX)}…` : s);

export function describeBoundDevicesProblems(
  problems: { issues: BindParamIssue[]; missing: Array<{ slot: string; token: string }>; failed: Array<{ slot: string; token: string }> },
  w: BoundDevicesWording,
): string[] {
  const out: string[] = [];
  for (const i of problems.issues) {
    const slot = echo(i.slot);
    if (i.kind === 'unknown-slot') out.push(w.unknownSlot(slot));
    else if (i.kind === 'not-a-device-slot') out.push(w.notADeviceSlot(slot));
    else if (i.kind === 'duplicate-slot') out.push(w.duplicateSlot(slot));
    else if (i.kind === 'derived-slot') out.push(w.derivedSlot(slot));
    else if (i.kind === 'invalid-token') out.push(w.invalidToken(slot, echo(i.token)));
    else {
      // A new kind must be worded, not shown as some other kind's sentence.
      const unhandled: never = i;
      throw new Error(`describeBoundDevicesProblems: unworded link issue ${JSON.stringify(unhandled)}`);
    }
  }
  for (const m of problems.missing) out.push(w.missing(m.slot, m.token));
  for (const f of problems.failed) out.push(w.failed(f.slot, f.token));
  return out;
}
