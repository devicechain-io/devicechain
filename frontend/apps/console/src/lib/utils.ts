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

/**
 * Decode a JSON object string, treating null, garbage and a non-object (a bare array,
 * number or string) alike as "empty".
 *
 * Tolerant on purpose: every caller reads an operator-authored setting whose write path
 * validates it, so a value that fails here predates that gate. Refusing would break the
 * screen; `{}` degrades to the built-in defaults, which is what the absence of the
 * setting already means.
 */
export function parseJsonObject(raw: string | null | undefined): Record<string, unknown> {
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    return parsed as Record<string, unknown>;
  } catch {
    return {};
  }
}

// ── Optional form fields ────────────────────────────────────────────────
//
// 🔴 These return NULL, never undefined, and that is the whole point. The requests
// they feed are full REPLACES whose update signatures take `Required<…>`, so an
// omitted key is written as NULL server-side anyway — saying it explicitly makes the
// request describe what the server will do, and makes `Required<…>` able to catch a
// field nobody carried.
//
// They live here because two forms had grown their own copies with the SAME names and
// DIFFERENT behaviour: one guarded against a non-numeric entry and one did not, so
// typing "abc" into a metric definition sent NaN to the API while the same typo in a
// tenant form sent null. That divergence is the reason to share them, not the line
// count.

/** Optional trimmed string → `null` when blank. */
export function optText(s: string): string | null {
  return s.trim() === '' ? null : s.trim();
}

/**
 * Optional numeric text → `null` when blank OR not a finite number.
 *
 * Treating unparseable input as "no override" rather than NaN is deliberate: NaN
 * serializes to `null` in JSON anyway, so the alternative is not a rejection but the
 * same clear by a less obvious route.
 */
export function optNum(s: string): number | null {
  const n = Number(s.trim());
  return s.trim() === '' || !Number.isFinite(n) ? null : n;
}
