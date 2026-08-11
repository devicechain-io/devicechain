// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A minimal styled native <select> for CLOSED enums — a fixed vocabulary the user
// picks from rather than types into. For an open set (a facet value, an existing
// token) reach for Combobox or SuggestField instead; those allow free text, which is
// the whole distinction.
//
// Native on purpose: a `<select>` gets keyboard interaction, type-ahead, screen-reader
// semantics and the platform's own touch picker for free, none of which a div-based
// listbox reproduces without effort.
//
// This began as three identical local copies — the rules canvas inspector, the
// connector config form, and the basemap provider picker — the second of which
// carried the comment "matching the canvas inspector's (there is no shared Select
// primitive)". There is one now.

import type { ReactNode } from 'react';

interface SelectProps {
  value: string;
  onChange: (v: string) => void;
  children: ReactNode;
  id?: string;
  /** Accessible name when the control has no visible <label> bound to it. */
  ariaLabel?: string;
  disabled?: boolean;
}

export function Select({ value, onChange, children, id, ariaLabel, disabled }: SelectProps) {
  return (
    <select
      id={id}
      value={value}
      aria-label={ariaLabel}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
      className="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
    >
      {children}
    </select>
  );
}
