// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pick exactly one of N — theme, locale, authoring mode, preview window, member
// family, tier colour. Seven of these were hand-rolled across the console, each a
// `.map()` over `<button>` carrying its own copy of the same active/inactive pair.
//
// 🔴 THE STYLING WAS THE VISIBLE DUPLICATION; THE SEMANTICS WERE THE ACTUAL DEFECT.
// "Pick one of N" is a RADIO GROUP. Of the seven sites, three announced a selection at
// all (via aria-pressed, which is the wrong role for a mutually exclusive set anyway);
// the theme toggle, the authoring-mode switch, the preview-window picker and the family
// filters told a screen reader nothing about which option was current. That is
// invisible in a screenshot, survives review, and is fixed here once for every caller.
//
// So this is built on @radix-ui/react-radio-group — already a direct dependency, so no
// new package — which supplies role="radiogroup" on the track, role="radio" +
// aria-checked on each segment, and a roving tabindex.
//
// 🔴 THAT ROVING TABINDEX IS A DELIBERATE BEHAVIOUR CHANGE, not an accident of the
// library. Before: each segment was its own tab stop and arrow keys did nothing. Now:
// the group is ONE tab stop and arrow keys move between segments, selecting as they go.
// That is how native radios behave and what WAI-ARIA prescribes — but it does mean
// arrowing across the authoring-mode switch re-initialises the editor at each step,
// exactly as clicking through the modes already did.
//
// 🔴 IT IS THE WRONG CONTROL FOR A SET THAT CAN BE EMPTY. A radio group cannot be
// cleared, so a picker where clicking the current option deselects it (the appearance
// icon grid) is a set of pressed buttons instead — see ToggleButton, which exists for
// exactly that boundary.
//
// Segment content is a ReactNode, so a caller can render more than a label — the locale
// switcher puts a code badge next to the endonym. Each segment carries Radix's
// `data-state`, so that badge restyles itself on selection through
// `group-data-[state=checked]:…` rather than needing a render prop.

import * as React from 'react';
import * as RadioGroupPrimitive from '@radix-ui/react-radio-group';

import { cn } from '@/lib/utils';

export interface SegmentedOption<T extends string> {
  value: T;
  /** Segment content. May be an icon, a label, or both. */
  label: React.ReactNode;
  /**
   * Accessible name. Set it whenever `label` renders no text of its own (an icon-only
   * segment) — an unnamed control is the case a visual check cannot catch. `title`
   * would also name it (it is the last-resort source in the accessible-name
   * algorithm), but only this one states the intent.
   */
  ariaLabel?: string;
  /** Native tooltip. Usually the same text as `ariaLabel` on an icon-only segment. */
  title?: string;
  disabled?: boolean;
  /** Per-segment styling, appended last. The only way to style a `bare` segment. */
  className?: string;
}

/**
 * 'inset'  — bordered track with padding; each segment a rounded pill inside it, the
 *            selected one filled. The chrome-control look (theme, locale).
 * 'joined' — segments butt against each other inside one clipped, bordered box.
 * 'loose'  — no track at all: free-standing pills that wrap, the selected one tinted.
 *            For a filter row whose option count is data-driven.
 * 'bare'   — no track and NO segment styling: focus ring and disabled treatment only.
 *            For a picker whose options each look different (a colour palette), where
 *            this contributes the radio-group semantics and nothing else.
 */
export type SegmentedTone = 'inset' | 'joined' | 'loose' | 'bare';

export interface SegmentedControlProps<T extends string> {
  options: readonly SegmentedOption<T>[];
  value: T;
  onValueChange: (value: T) => void;
  /** Names the group itself, e.g. "Theme". Radix puts it on the radiogroup. */
  ariaLabel: string;
  tone?: SegmentedTone;
  /** Ignored by `bare`, which carries no padding of its own. */
  size?: 'sm' | 'md';
  /** Segments share the width equally and the track spans its container. */
  fill?: boolean;
  /** Rendered inside the track, before the first segment (e.g. a category icon). */
  leading?: React.ReactNode;
  disabled?: boolean;
  className?: string;
}

const TONE: Record<
  SegmentedTone,
  { track: string; segment: string; checked: string; unchecked: string; sized: boolean }
> = {
  inset: {
    track: 'items-center gap-1 rounded-md border border-border bg-background p-0.5',
    segment: 'rounded-sm',
    checked: 'bg-primary text-primary-foreground',
    unchecked: 'text-muted-foreground hover:text-foreground',
    sized: true,
  },
  joined: {
    track: 'overflow-hidden rounded-md border border-border',
    segment: '',
    checked: 'bg-primary text-primary-foreground',
    unchecked: 'text-muted-foreground hover:bg-muted hover:text-foreground',
    sized: true,
  },
  loose: {
    track: 'flex-wrap gap-2',
    segment: 'rounded-full border',
    checked: 'border-primary bg-primary/10 font-medium text-primary',
    unchecked: 'border-border text-muted-foreground hover:bg-muted',
    sized: true,
  },
  bare: {
    track: 'flex-wrap gap-2',
    segment: '',
    checked: '',
    unchecked: '',
    sized: false,
  },
};

const SIZE = {
  sm: 'h-7 px-2 text-xs',
  md: 'h-8 px-3 text-sm',
} as const;

export function SegmentedControl<T extends string>({
  options,
  value,
  onValueChange,
  ariaLabel,
  tone = 'inset',
  size = 'sm',
  fill = false,
  leading,
  disabled,
  className,
}: SegmentedControlProps<T>) {
  const look = TONE[tone];
  return (
    <RadioGroupPrimitive.Root
      value={value}
      // Radix's own value type is `string`; the generic is ours, so the cast sits at
      // the boundary and every caller above it stays exhaustively typed.
      onValueChange={(v) => onValueChange(v as T)}
      orientation="horizontal"
      loop
      disabled={disabled}
      aria-label={ariaLabel}
      className={cn(fill ? 'flex' : 'inline-flex', look.track, className)}
    >
      {leading}
      {options.map((opt) => (
        <RadioGroupPrimitive.Item
          key={opt.value}
          value={opt.value}
          disabled={opt.disabled}
          aria-label={opt.ariaLabel}
          title={opt.title}
          className={cn(
            'group flex items-center justify-center gap-1.5 whitespace-nowrap transition-colors',
            'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
            'disabled:cursor-not-allowed disabled:opacity-50',
            look.sized && SIZE[size],
            look.segment,
            value === opt.value ? look.checked : look.unchecked,
            fill && 'flex-1',
            opt.className,
          )}
        >
          {opt.label}
        </RadioGroupPrimitive.Item>
      ))}
    </RadioGroupPrimitive.Root>
  );
}
