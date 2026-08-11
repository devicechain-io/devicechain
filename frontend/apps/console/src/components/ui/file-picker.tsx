// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A file chooser that looks like the rest of the kit.
//
// 🔴 THE NATIVE ELEMENT IS UNAVOIDABLE HERE, AND THAT IS EXACTLY WHY THIS EXISTS.
// Opening a file dialog requires a real `<input type="file">` activated by a genuine
// user gesture — no library replaces that, and no amount of styling reaches the
// dialog itself. What CAN be owned is everything the user sees: the button.
//
// So the input is rendered hidden and driven by a Button. That is the standard shape,
// and the reason to put it in the kit rather than write it per page is the one that
// motivates the whole sweep: it was written inline once, it would be written inline
// again, and the two copies would drift. Here there is one place to change how
// picking a file looks, and the raw element is confined to a file the lint rule
// already exempts.
//
// Note the `value = ''` reset in onChange: without it, choosing the SAME file twice
// in a row fires no second change event, because the input's value has not changed —
// a bug that only shows up when someone re-uploads a corrected file and nothing
// happens.

import { useRef, type ReactNode } from 'react';
import { Button } from '@/components/ui/button';

export interface FilePickerProps {
  onPick: (file: File) => void;
  /** Passed straight to the input, e.g. 'image/png,image/jpeg'. */
  accept?: string;
  disabled?: boolean;
  /** Button content — icon and label. */
  children: ReactNode;
  variant?: React.ComponentProps<typeof Button>['variant'];
  size?: React.ComponentProps<typeof Button>['size'];
}

export function FilePicker({
  onPick,
  accept,
  disabled,
  children,
  variant = 'outline',
  size = 'sm',
}: FilePickerProps) {
  const ref = useRef<HTMLInputElement>(null);
  return (
    <>
      <input
        ref={ref}
        type="file"
        accept={accept}
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) onPick(file);
          e.target.value = '';
        }}
      />
      <Button
        type="button"
        variant={variant}
        size={size}
        disabled={disabled}
        onClick={() => ref.current?.click()}
      >
        {children}
      </Button>
    </>
  );
}
