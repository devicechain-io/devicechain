// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The from-to picker for an absolute time range. Its own chunk: the time-range bar imports it on the
// first click of "Custom", so a board that never opens it never downloads it (and the bar itself,
// which every board with a header carries, stays small).
//
// The two fields are the browser's own `datetime-local` inputs, which read and write the VIEWER'S
// zone. That zone is printed beside them, always, because "02:00" means nothing without one; the
// window is stored as instants, and the link it offers is in UTC.

import type { AbsoluteRange, AbsoluteRangeProblem } from '@devicechain/dashboards';
import { useId, useState, type CSSProperties } from 'react';

export interface AbsolutePickerLabels {
  custom: string;
  from: string;
  to: string;
  apply: string;
  cancel: string;
  // "Times are in Europe/Berlin (UTC+02:00). Shared links use UTC."
  zone: (zone: string, offset: string) => string;
  problem: Record<AbsoluteRangeProblem, string>;
}

const pad = (n: number) => String(n).padStart(2, '0');

// A `datetime-local` value (`2026-10-22T14:30`) for an instant, in the viewer's zone.
export function toLocalInput(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// The instant for a `datetime-local` value read in the viewer's zone, or NaN for an empty/invalid one.
export function fromLocalInput(value: string): number {
  return value ? new Date(value).getTime() : Number.NaN;
}

export function zoneInfo(at: number = Date.now()): { zone: string; offset: string } {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'local';
  const minutes = -new Date(at).getTimezoneOffset();
  const sign = minutes < 0 ? '-' : '+';
  const abs = Math.abs(minutes);
  return { zone, offset: `UTC${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}` };
}

const field: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: 2,
  fontSize: 12,
  flex: '1 1 13rem',
  minWidth: 0,
};
const input: CSSProperties = {
  font: 'inherit',
  fontSize: 13,
  padding: '3px 6px',
  color: 'hsl(var(--foreground))',
  background: 'hsl(var(--background))',
  border: '1px solid hsl(var(--border))',
  borderRadius: 4,
  minWidth: 0,
};
const action = (primary: boolean): CSSProperties => ({
  padding: '3px 10px',
  fontSize: 12,
  cursor: 'pointer',
  color: primary ? 'hsl(var(--background))' : 'hsl(var(--foreground))',
  background: primary ? 'hsl(var(--foreground))' : 'transparent',
  border: '1px solid hsl(var(--border))',
  borderRadius: 4,
});

export default function AbsoluteRangePicker({
  initial,
  labels,
  onApply,
  onCancel,
}: {
  initial?: AbsoluteRange;
  labels: AbsolutePickerLabels;
  // Returns why the window was refused, or undefined when it was taken.
  onApply: (fromMs: number, toMs: number) => AbsoluteRangeProblem | undefined;
  onCancel: () => void;
}) {
  const id = useId();
  const now = Date.now();
  // Open on the window already in force, or on the last hour as a starting point to edit.
  const [from, setFrom] = useState(() => toLocalInput(initial?.fromMs ?? now - 60 * 60 * 1000));
  const [to, setTo] = useState(() => toLocalInput(initial?.toMs ?? now));
  const [problem, setProblem] = useState<AbsoluteRangeProblem | undefined>();
  const { zone, offset } = zoneInfo();
  const max = toLocalInput(now);

  const submit = () => {
    const p = onApply(fromLocalInput(from), fromLocalInput(to));
    setProblem(p);
  };

  return (
    <form
      data-absolute-picker=""
      aria-label={labels.custom}
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      onKeyDown={(e) => {
        if (e.key === 'Escape') onCancel();
      }}
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
        padding: 10,
        boxSizing: 'border-box',
        width: 'min(24rem, calc(100vw - 32px))',
        color: 'hsl(var(--foreground))',
        background: 'hsl(var(--background))',
        border: '1px solid hsl(var(--border))',
        borderRadius: 6,
        boxShadow: '0 4px 16px rgba(0,0,0,0.18)',
      }}
    >
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <label htmlFor={`${id}-from`} style={field}>
          {labels.from}
          <input
            id={`${id}-from`}
            data-absolute-from=""
            type="datetime-local"
            value={from}
            max={max}
            required
            onChange={(e) => setFrom(e.target.value)}
            style={input}
          />
        </label>
        <label htmlFor={`${id}-to`} style={field}>
          {labels.to}
          <input
            id={`${id}-to`}
            data-absolute-to=""
            type="datetime-local"
            value={to}
            max={max}
            required
            onChange={(e) => setTo(e.target.value)}
            style={input}
          />
        </label>
      </div>
      <div data-absolute-zone="" style={{ fontSize: 12, color: 'hsl(var(--muted-foreground))' }}>
        {labels.zone(zone, offset)}
      </div>
      {problem ? (
        <div role="alert" data-absolute-problem={problem} style={{ fontSize: 12, color: 'hsl(var(--destructive))' }}>
          {labels.problem[problem]}
        </div>
      ) : null}
      <div style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
        <button type="button" onClick={onCancel} style={action(false)}>
          {labels.cancel}
        </button>
        <button type="submit" data-absolute-apply="" style={action(true)}>
          {labels.apply}
        </button>
      </div>
    </form>
  );
}
