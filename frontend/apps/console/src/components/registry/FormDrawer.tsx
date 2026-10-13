// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A right-side slide-out drawer for create (and other short) forms — the standard
// "enter new data" surface across the console. Built on the Sheet primitive; the
// form it wraps keeps its own submit button and calls back on success, at which
// point the opener closes the drawer and refreshes its list.

import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet';

export function FormDrawer({
  open,
  onOpenChange,
  title,
  description,
  children,
  contentClassName,
  shot,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
  /** Overrides the default drawer width — e.g. a wide surface for the canvas editor. */
  contentClassName?: string;
  /** Stable kebab-case id for the user-guide capture (data-shot-drawer); the title is translated, so it cannot be selected on. */
  shot?: string;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" data-shot="drawer" data-shot-drawer={shot} className={cn('w-full overflow-y-auto sm:max-w-lg', contentClassName)}>
        <SheetHeader className="mb-6">
          <SheetTitle>{title}</SheetTitle>
          {description && <SheetDescription>{description}</SheetDescription>}
        </SheetHeader>
        {children}
      </SheetContent>
    </Sheet>
  );
}
