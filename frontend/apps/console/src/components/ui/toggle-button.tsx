// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A control that is ON or OFF and can be BOTH — an appearance icon that clicking again
// clears, a preview firing whose selection toggles the canvas overlay.
//
// 🔴 THE DISTINCTION FROM SegmentedControl IS SEMANTIC, NOT VISUAL, AND IT DECIDES THE
// MARKUP. "Pick exactly one of N" is a radio group: one tab stop, arrow keys, and no
// way to end up with nothing selected. "Toggle this" is a pressed button: its own tab
// stop, space/enter, and clearable. Choosing by appearance rather than behaviour is how
// a row of filters ends up announcing itself as a set of independent switches, or a
// clearable picker ends up in a group that cannot be cleared.
//
// It is deliberately almost unstyled — `pressed` is required, and the focus ring and
// disabled treatment come from the kit, but the shape is the caller's. That is the
// honest position at two callers: there is no duplication for a variant to prevent,
// only the semantics to get right and one place to change them from. The moment a third
// caller wants the same shape as one of these, the shape moves in here.

import * as React from 'react';

import { cn } from '@/lib/utils';

export interface ToggleButtonProps
  extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, 'type' | 'aria-pressed'> {
  pressed: boolean;
}

export const ToggleButton = React.forwardRef<HTMLButtonElement, ToggleButtonProps>(
  ({ pressed, className, ...props }, ref) => (
    <button
      ref={ref}
      type="button"
      aria-pressed={pressed}
      className={cn(
        'transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
        'disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    />
  ),
);
ToggleButton.displayName = 'ToggleButton';
