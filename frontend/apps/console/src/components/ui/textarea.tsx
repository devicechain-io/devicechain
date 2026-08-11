// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The multi-line counterpart to Input, mirroring its border, focus ring and
// disabled treatment so a form reads as one control set rather than two.
//
// It lived in `routes/common.tsx` until now, which is the same inconsistency the
// native-element sweep is about, one level up: a shared control parked outside the
// kit is a control nobody finds, so the next person writes another one. Anything
// every page can use belongs in `components/ui/`.
//
// 🔴 `font-mono` is in the BASE class list, not an opt-in, because every current
// caller is editing something machine-shaped — a JSON settings blob, a GeoJSON
// geometry, a rules expression, a connector config. Kept as the default to preserve
// exactly how those four render today; pass `className="font-sans"` for prose.

import type { TextareaHTMLAttributes } from 'react';
import { cn } from '@/lib/utils';

export type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement>;

export function Textarea({ className, ...props }: TextareaProps) {
  return (
    <textarea
      className={cn(
        'flex min-h-20 w-full rounded-md border border-input bg-background px-3 py-2 text-sm font-mono',
        'ring-offset-background placeholder:text-muted-foreground',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
        'disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    />
  );
}
