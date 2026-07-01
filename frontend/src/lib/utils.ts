// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { type ClassValue, clsx } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// tailwind-merge doesn't know our custom @theme font-size tokens and would
// classify `text-label` etc. as text COLORS — silently dropping them whenever
// cn() composes a size with a real color (e.g. cn('text-label', 'text-cyan-300')).
// Registering them in the font-size group keeps size + color independent,
// matching how the old `text-[10px]` arbitrary values merged.
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": ["text-micro", "text-label", "text-label-lg"],
    },
  },
});

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** Clamp a number to the inclusive [min, max] range. */
export function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

/** Render an ISO timestamp in the local locale, or an em dash when absent. */
export function formatTime(value?: string | null): string {
  return value ? new Date(value).toLocaleString() : '—';
}