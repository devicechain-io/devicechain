// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A colour picker that lives inside our theme.
//
// 🔴 THIS IS THE HARDEST CASE FOR "NO NATIVE CONTROLS", AND THE ONE MOST WORTH DOING.
// `<input type="color">` looks like the obvious answer — it is one element, it is
// accessible, and it gives you the platform picker for free. But the picker it opens
// is an OPERATING SYSTEM DIALOG: it takes none of our theme, none of our typography,
// and on Linux/Windows/macOS it is three visually unrelated things. It is the same
// defect the native `<select>` had, in the surface where colour is literally the
// subject.
//
// So the picker is react-colorful (2.8 KB, no dependencies) rendered inside our own
// Popover, over markup we style. The trigger is a swatch button; the saturation
// square and hue slider are ours to theme; the surrounding panel is `bg-popover` like
// every other overlay in the kit.
//
// The panel carries BOTH a drag surface and a hex field, and the second is not a
// nicety: dragging is for exploring, typing is for entering a brand colour someone
// was handed. A picker that only supports dragging cannot accept "#0b3d2e" at all —
// which is the common case in a console where colours arrive from a brand guide.

import { useState } from 'react';
import { HexColorInput, HexColorPicker } from 'react-colorful';
import { cn } from '@/lib/utils';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';

// A 6-digit hex colour. The picker only ever emits this form; the guard is for the
// value arriving from storage or from the paired text field.
const HEX = /^#[0-9a-fA-F]{6}$/;

export function isHexColor(v: string): boolean {
  return HEX.test(v);
}

export interface ColorPickerProps {
  /** The current colour as `#rrggbb`. Anything else renders the fallback swatch. */
  value: string;
  onChange: (hex: string) => void;
  /** Shown in the swatch when `value` is not a usable colour. */
  fallback?: string;
  /** Accessible name — required, since the trigger's only content is a colour. */
  ariaLabel: string;
  id?: string;
  disabled?: boolean;
  className?: string;
}

export function ColorPicker({
  value,
  onChange,
  fallback = '#000000',
  ariaLabel,
  id,
  disabled,
  className,
}: ColorPickerProps) {
  const [open, setOpen] = useState(false);
  const shown = isHexColor(value) ? value : fallback;

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          id={id}
          type="button"
          disabled={disabled}
          aria-label={ariaLabel}
          className={cn(
            'size-9 shrink-0 cursor-pointer rounded border border-input p-0.5',
            'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
            'disabled:cursor-not-allowed disabled:opacity-50',
            className,
          )}
        >
          {/* The swatch itself. An inline style because the colour is DATA — a
              Tailwind class cannot express a value chosen at runtime. */}
          <span className="block size-full rounded-sm" style={{ backgroundColor: shown }} />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-auto p-3" align="start">
        <div className="flex flex-col gap-2">
          {/* react-colorful is styled through its own class names; `dc-color-picker`
              sizes it to the panel. See index.css. */}
          <HexColorPicker color={shown} onChange={onChange} className="dc-color-picker" />
          <HexColorInput
            color={shown}
            onChange={onChange}
            prefixed
            aria-label={ariaLabel}
            className={cn(
              'h-8 w-full rounded-md border border-input bg-transparent px-2 text-sm font-mono',
              'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
            )}
          />
        </div>
      </PopoverContent>
    </Popover>
  );
}
