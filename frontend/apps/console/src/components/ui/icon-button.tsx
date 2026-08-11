// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A small icon-only action — close a panel, copy a value, grab a drag handle.
//
// 🔴 `label` IS REQUIRED BY THE TYPE, AND THAT IS THE POINT OF THE COMPONENT.
// An icon button's defect is not its styling, it is that it has no accessible name:
// the glyph carries the meaning visually and nothing carries it otherwise. Reviewing
// for a missing aria-label is exactly the kind of check that passes until it doesn't,
// so here it is a compile error instead. (`Button variant="ghost" size="icon"` exists
// and cannot make that demand — its content is usually text.)
//
// The size scale is smaller than Button's `icon` (which is 40px, sized to sit beside a
// default Button in a toolbar). These sit inline against text and table cells, where
// 40px is a hit target that pushes the row taller than its content.

import * as React from 'react';

import { cn } from '@/lib/utils';

export interface IconButtonProps
  extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, 'aria-label' | 'title' | 'type'> {
  /** The accessible name. Required — an icon has no text of its own. */
  label: string;
  /**
   * Also expose `label` as a native tooltip. On by default: if a glyph needs a name for
   * assistive tech, a sighted user generally needs it too.
   */
  tooltip?: boolean;
  size?: 'xs' | 'sm' | 'md';
  /**
   * 'ghost' — fills with the accent colour on hover; for a control sitting on its own.
   * 'quiet' — brightens only; for a control tucked beside text, where a hover fill
   *           would draw a box around part of a sentence.
   */
  variant?: 'ghost' | 'quiet';
}

const SIZE = {
  xs: 'size-5',
  sm: 'size-6',
  md: 'size-8',
} as const;

const VARIANT = {
  ghost: 'text-muted-foreground hover:bg-accent hover:text-foreground',
  quiet: 'text-muted-foreground hover:text-foreground',
} as const;

export const IconButton = React.forwardRef<HTMLButtonElement, IconButtonProps>(
  ({ label, tooltip = true, size = 'sm', variant = 'ghost', className, ...props }, ref) => (
    <button
      ref={ref}
      type="button"
      aria-label={label}
      title={tooltip ? label : undefined}
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-md transition-colors',
        'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
        'disabled:cursor-not-allowed disabled:opacity-50',
        SIZE[size],
        VARIANT[variant],
        className,
      )}
      {...props}
    />
  ),
);
IconButton.displayName = 'IconButton';
