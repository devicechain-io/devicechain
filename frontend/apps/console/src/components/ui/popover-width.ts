// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The width of a dropdown opened from a field (Combobox, MultiSelect): as wide as the field, never
// narrower than 14rem (a narrow list cell's options would otherwise truncate to a few letters), and
// never wider than the room the viewport has left, so a long option name is cut with an ellipsis
// instead of running off a phone screen.
//
// 🔴 The var() is load-bearing. Tailwind v4 compiles a bare custom property in brackets (`w-[--name]`) to the
// literal `width: --name`, which the browser drops; tailwind-merge had also
// removed PopoverContent's own `w-72`, so the dropdown grew to its longest option, past the
// viewport at 390 px.
export const FIELD_POPOVER_WIDTH =
  'w-[var(--radix-popover-trigger-width)] min-w-[min(14rem,var(--radix-popover-content-available-width))] max-w-[var(--radix-popover-content-available-width)]';
