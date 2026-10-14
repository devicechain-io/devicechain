// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The age readout: "12 sec. ago", ticking once a second, plus a "stale" badge when -- and only
// when -- a threshold is known and passed. The frame draws it for any widget whose descriptor
// opts in (`freshness: true`); a custom widget that wants it somewhere else can render
// <Freshness> itself.

import { useEffect, useState, type CSSProperties } from 'react';

import { ageSeconds, formatAge, freshnessStrings, isStale } from './freshness';
import { css } from './theme';

// useNow re-renders its caller every `everyMs` with the current time. One second is the finest
// unit the readout shows.
export function useNow(everyMs = 1000, enabled = true): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(id);
  }, [everyMs, enabled]);
  return now;
}

export interface FreshnessReading {
  age: number;
  stale: boolean;
}

// useFreshness: the age of `at` (epoch ms) now, and whether it is stale against the threshold.
// Undefined while there is no sample to age.
export function useFreshness(at: number | undefined, staleAfterSeconds: number | undefined): FreshnessReading | undefined {
  // No sample, no clock: a frame that does not opt in must not tick once a second.
  const now = useNow(1000, at !== undefined);
  if (at === undefined) return undefined;
  const age = ageSeconds(at, now);
  return { age, stale: isStale(age, staleAfterSeconds) };
}

export function StaleBadge({ label }: { label?: string }) {
  return (
    <span
      data-stale-badge=""
      style={{
        fontSize: 10,
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
        fontSize: 11,
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
