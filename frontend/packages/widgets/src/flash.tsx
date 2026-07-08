// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Directional change-flash — an opt-in cue (widget option `flashOnChange`) that briefly
// tints a numeric value green when it rises and red when it falls, then fades back, so a
// glance catches direction of change on a live board (cf. a trading "last" column). Purely
// presentational: no hub/model change, driven off the value the widget already renders.
//
// Single-value widgets (latest-card) color the number; a multi-row widget (table,
// alarm-table) renders each value through <FlashValue>, so React's per-key component
// instances give every row its own independent flash with no map bookkeeping. The gauge
// (an opaque ECharts canvas whose number can't be CSS-transitioned) tints its container
// instead — see flashBackgroundStyle.

import { useEffect, useRef, useState, type CSSProperties } from 'react';

import { formatValue } from './format';

export type FlashDirection = 'up' | 'down' | null;

// Semantic up/down colors, mid-saturation so they read on both light and dark host themes;
// a host may override with the --flash-up / --flash-down custom properties. The background
// variants are translucent so a tint sits over the widget's own surface without hiding it.
const UP = {
  fg: 'var(--flash-up, hsl(142 72% 40%))',
  bg: 'var(--flash-up-bg, hsl(142 72% 45% / 0.16))',
};
const DOWN = {
  fg: 'var(--flash-down, hsl(0 72% 51%))',
  bg: 'var(--flash-down-bg, hsl(0 72% 55% / 0.16))',
};

// How long (ms) the solid tint holds before fading, and how long the fade back takes.
const HOLD_MS = 400;
const FADE_MS = 600;

// useFlashOnChange watches a numeric value and returns the direction of its most recent
// change ('up' | 'down') for a short window, then null. It stays null when the feature is
// off, on the first value (no flash on mount), on a non-finite value, or when `identity`
// changes — the last so a widget bound to an ANCHOR (whose one measurement slot interleaves
// values from many member devices) adopts the new device's value instead of reading the
// device-to-device jump as a real rise/fall. Pass the value's owning entity as `identity`
// (e.g. `deviceToken:name`); leave it undefined for a single-device value.
//
// The held direction auto-clears after HOLD_MS; the consumer fades it out via flashTextStyle.
// Every early-return path resets `direction` too: the effect only re-runs on a dep change,
// and each such change has already cancelled the pending timer, so not clearing here would
// strand a tint (e.g. value → null, or toggled off then on, mid-hold).
export function useFlashOnChange(
  value: number | null | undefined,
  enabled: boolean,
  identity?: string | number,
): FlashDirection {
  const prev = useRef<number | null | undefined>(value);
  const prevIdentity = useRef(identity);
  const [direction, setDirection] = useState<FlashDirection>(null);

  useEffect(() => {
    const before = prev.current;
    const sameEntity = prevIdentity.current === identity;
    prev.current = value;
    prevIdentity.current = identity;
    if (
      !enabled ||
      !sameEntity ||
      typeof value !== 'number' ||
      typeof before !== 'number' ||
      !Number.isFinite(value) ||
      !Number.isFinite(before) ||
      value === before
    ) {
      setDirection(null); // a no-op when already null (React bails out); clears a stale tint otherwise
      return;
    }
    setDirection(value > before ? 'up' : 'down');
    const timer = setTimeout(() => setDirection(null), HOLD_MS);
    return () => clearTimeout(timer);
  }, [value, enabled, identity]);

  // Mask any lingering state when disabled, so toggling off is immediate.
  return enabled ? direction : null;
}

// flashTextStyle colors text for the current direction: snap to the tint (no transition)
// while flashing, then fade `color` back to the element's own color when it clears. Merge
// it AFTER an explicit base `color` so the tint overrides while set and the base shows
// through (and animates) once cleared.
export function flashTextStyle(direction: FlashDirection, fadeMs: number = FADE_MS): CSSProperties {
  if (direction) return { color: direction === 'up' ? UP.fg : DOWN.fg, transition: 'none' };
  return { transition: `color ${fadeMs}ms ease-out` };
}

// flashBackgroundStyle is the same cue as a translucent background tint, for a widget whose
// value can't be recolored directly (the gauge's canvas). Snap in, fade the background back.
export function flashBackgroundStyle(direction: FlashDirection, fadeMs: number = FADE_MS): CSSProperties {
  if (direction) {
    return { backgroundColor: direction === 'up' ? UP.bg : DOWN.bg, transition: 'none' };
  }
  return { transition: `background-color ${fadeMs}ms ease-out` };
}

// FlashValue renders a formatted numeric value that flashes on change — the reusable cell
// for multi-row widgets. Each rendered instance owns its own flash state, so keying rows by
// entity (React's list key) gives every row an independent cue for free.
export function FlashValue({
  value,
  precision,
  enabled,
  identity,
  baseColor = 'inherit',
  style,
}: {
  value: number | null | undefined;
  precision?: number;
  enabled: boolean;
  // The value's owning entity (see useFlashOnChange): when it changes, adopt the new value
  // rather than flash. Distinguishes a real per-device change from an anchor's cross-device
  // interleave. Undefined for a single-device value.
  identity?: string | number;
  // The color the value fades back to (defaults to the inherited text color).
  baseColor?: string;
  style?: CSSProperties;
}) {
  const direction = useFlashOnChange(value, enabled, identity);
  return (
    <span style={{ color: baseColor, ...style, ...flashTextStyle(direction) }}>
      {formatValue(value, precision)}
    </span>
  );
}
