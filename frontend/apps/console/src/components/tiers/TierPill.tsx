// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// TierPill renders a tier's colored pill (ADR-065 S5c) — the small badge shown beside a
// tenant so its packaging is legible at a glance. The tier's color is a palette TOKEN
// (see the server's iam.TierColors); this component owns the mapping from that token to
// an actual swatch, which is why the server stores only names: a restyle is a change
// here, not a migration.
//
// THE SWATCHES ARE FULL STATIC CLASS STRINGS, one per token, on purpose. Tailwind v4
// scans source text for class names at build time, so a computed class like
// `bg-${color}-500` is never generated and the pill would render unstyled. Each token
// therefore maps to a literal string the scanner can see. It also means the set here must
// stay in step with the server palette — the tokens are the contract, and an unknown one
// falls back to neutral rather than vanishing.
//
// Each swatch is a soft tint plus a saturated text/border, tuned to read in BOTH themes
// (the whole reason colors are a curated palette and not free hex): a light tint on the
// dark theme would be invisible, so the dark variants lift the text instead.

import { cn } from '@/lib/utils';

const SWATCHES: Record<string, string> = {
  slate: 'bg-slate-500/15 text-slate-700 dark:text-slate-300 ring-slate-500/30',
  gray: 'bg-gray-500/15 text-gray-700 dark:text-gray-300 ring-gray-500/30',
  red: 'bg-red-500/15 text-red-700 dark:text-red-300 ring-red-500/30',
  orange: 'bg-orange-500/15 text-orange-700 dark:text-orange-300 ring-orange-500/30',
  amber: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 ring-amber-500/30',
  green: 'bg-green-500/15 text-green-700 dark:text-green-300 ring-green-500/30',
  teal: 'bg-teal-500/15 text-teal-700 dark:text-teal-300 ring-teal-500/30',
  sky: 'bg-sky-500/15 text-sky-700 dark:text-sky-300 ring-sky-500/30',
  blue: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 ring-blue-500/30',
  violet: 'bg-violet-500/15 text-violet-700 dark:text-violet-300 ring-violet-500/30',
  fuchsia: 'bg-fuchsia-500/15 text-fuchsia-700 dark:text-fuchsia-300 ring-fuchsia-500/30',
  rose: 'bg-rose-500/15 text-rose-700 dark:text-rose-300 ring-rose-500/30',
};

// A tier with no color chosen ("") gets a neutral pill — still legible, just unbranded.
const NEUTRAL = 'bg-muted text-muted-foreground ring-border';

// tierSwatch returns the swatch classes for a palette token, falling back to neutral for
// "" or an unknown token. Exported so a color PICKER can render the same swatch it will
// store.
export function tierSwatch(color: string | null | undefined): string {
  if (!color) return NEUTRAL;
  return SWATCHES[color] ?? NEUTRAL;
}

export function TierPill({
  label,
  color,
  className,
}: {
  // The text on the pill — the tier's TOKEN. Kept lowercase and short on purpose: the
  // pill is the tier's stable identifier + color, and the human-facing NAME (which may be
  // capitalized or absent) is shown separately beside it. Passing the token keeps every
  // pill consistent — a mix of names and tokens made some pills capitalized and some not.
  label: string;
  color: string | null | undefined;
  className?: string;
}) {
  return (
    <span
      className={cn(
        // Fixed min-width + centered so a column of pills lines up regardless of token
        // length; lowercase because tokens are (a token cannot start upper, but this
        // makes the intent explicit and survives any future display name slipping in).
        'inline-flex min-w-[4.5rem] items-center justify-center rounded-full px-2 py-0.5 text-xs font-medium lowercase ring-1 ring-inset',
        tierSwatch(color),
        className,
      )}
    >
      {label}
    </span>
  );
}
