// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The age readout: "12 sec. ago", ticking once a second, plus a "stale" badge when -- and only
// when -- a threshold is known and passed. The frame draws it for any widget whose descriptor
// opts in (`freshness: true`); a custom widget that wants it somewhere else can render
// <Freshness> itself.

import { useEffect, useState, type CSSProperties } from 'react';

import { ageSeconds, formatAge, freshnessStrings, isStale } from './freshness';
import { css } from './theme';
import { fontVar } from './tokens';

// One interval per period, shared by every subscriber: a board of two hundred widgets ticks
// once a second, not two hundred times, and every readout on it moves together. The timer
// exists only while something is subscribed.
interface Ticker {
  timer: ReturnType<typeof setInterval>;
  listeners: Set<(now: number) => void>;
}
const tickers = new Map<number, Ticker>();

function subscribeTick(everyMs: number, listener: (now: number) => void): () => void {
  let ticker = tickers.get(everyMs);
  if (!ticker) {
    const listeners = new Set<(now: number) => void>();
    const timer = setInterval(() => {
      const now = Date.now();
      for (const l of [...listeners]) l(now);
    }, everyMs);
    ticker = { timer, listeners };
    tickers.set(everyMs, ticker);
  }
  ticker.listeners.add(listener);
  const owned = ticker;
  return () => {
    owned.listeners.delete(listener);
    if (owned.listeners.size === 0 && tickers.get(everyMs) === owned) {
      clearInterval(owned.timer);
      tickers.delete(everyMs);
    }
  };
}

// useNow re-renders its caller every `everyMs` with the current time, off the shared ticker
// for that period. One second is the finest unit the readout shows.
export function useNow(everyMs = 1000, enabled = true): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    setNow(Date.now());
    return subscribeTick(everyMs, setNow);
  }, [everyMs, enabled]);
  return now;
}

export interface FreshnessReading {
  age: number;
  stale: boolean;
}

// useFreshness: the age of `at` (epoch ms) now, and whether it is stale against the threshold.
// Undefined while there is no sample to age.
//
// 🔴 A sample time AHEAD of the viewer's clock (a device clock running fast) is aged from when
// this viewer first saw it, not from the time it claims. Clamping the age at zero alone would
// read "now" for as long as the device is ahead -- an hour-fast clock would keep a frozen
// value looking live for an hour, which is exactly what the readout exists to prevent.
export function useFreshness(at: number | undefined, staleAfterSeconds: number | undefined): FreshnessReading | undefined {
  // No sample, no clock: a frame that does not opt in must not tick once a second.
  const now = useNow(1000, at !== undefined);
  // When each distinct sample time was first seen here; adjusted during render (React's
  // "storing information from previous renders" pattern) so the first paint is already right.
  const [seen, setSeen] = useState<{ at: number; seenAt: number } | undefined>(undefined);
  let current = seen;
  if (at !== undefined && seen?.at !== at) {
    current = { at, seenAt: Math.max(now, Date.now()) };
    setSeen(current);
  }
  if (at === undefined || current === undefined) return undefined;
  const age = ageSeconds(Math.min(at, current.seenAt), now);
  return { age, stale: isStale(age, staleAfterSeconds) };
}

export function StaleBadge({ label }: { label?: string }) {
  return (
    <span
      data-stale-badge=""
      style={{
        fontSize: fontVar('xs'),
        fontWeight: 600,
        textTransform: 'uppercase',
        letterSpacing: 0.4,
        padding: '0 5px',
        borderRadius: 4,
        border: `1px dashed ${css('muted-foreground')}`,
        color: css('foreground'),
        background: css('muted'),
        whiteSpace: 'nowrap',
      }}
    >
      {label ?? freshnessStrings().stale}
    </span>
  );
}

export interface FreshnessProps {
  // The newest sample's time, epoch ms. Nothing renders without one.
  at: number | undefined;
  // The age past which the value is stale. Omit when unknown: the age is shown and nothing is
  // styled stale.
  staleAfterSeconds?: number;
  // Override the badge word (a host in a language the SDK does not ship).
  staleLabel?: string;
  style?: CSSProperties;
}

export function Freshness({ at, staleAfterSeconds, staleLabel, style }: FreshnessProps) {
  const reading = useFreshness(at, staleAfterSeconds);
  if (!reading) return null;
  return <FreshnessReadout reading={reading} staleLabel={staleLabel} style={style} />;
}

export function FreshnessReadout({
  reading,
  staleLabel,
  style,
}: {
  reading: FreshnessReading;
  staleLabel?: string;
  style?: CSSProperties;
}) {
  return (
    <span
      data-freshness=""
      data-stale={reading.stale ? 'true' : undefined}
      title={freshnessStrings().ageLabel}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        fontSize: fontVar('xs'),
        color: css('muted-foreground'),
        fontVariantNumeric: 'tabular-nums',
        ...style,
      }}
    >
      <time>{formatAge(reading.age)}</time>
      {reading.stale ? <StaleBadge label={staleLabel} /> : null}
    </span>
  );
}
