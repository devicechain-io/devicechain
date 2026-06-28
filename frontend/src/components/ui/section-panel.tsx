// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Ported from the ryft UI kit. A titled card for grouping related content
// within a page — the standard container for forms and detail sections. The
// optional `action` slot holds a section-level toolbar; `collapsible` makes the
// title toggle the body. (DeviceChain pages own their vertical rhythm via
// `space-y-*`, so unlike ryft's original this carries no bottom margin.)

import { useState, type ReactNode } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';

interface SectionPanelProps {
  title?: string;
  description?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  collapsible?: boolean;
  defaultCollapsed?: boolean;
}

export function SectionPanel({
  title,
  description,
  action,
  children,
  className,
  collapsible,
  defaultCollapsed,
}: SectionPanelProps) {
  const [open, setOpen] = useState(!defaultCollapsed);

  // The header renders when there's a title or an action to show.
  const header = (title || action) && (
    <div className={cn('flex flex-col', open && 'mb-4')}>
      <div className="flex items-center justify-between gap-3">
        {collapsible && title ? (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="flex items-center gap-1.5 text-lg font-semibold text-foreground transition-colors hover:text-muted-foreground"
          >
            {open ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
            {title}
          </button>
        ) : (
          <h2 className="text-lg font-semibold text-foreground">{title}</h2>
        )}
        {action && <div className="flex items-center gap-2">{action}</div>}
      </div>
      {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
    </div>
  );

  return (
    <section className={cn('rounded-lg border border-border bg-card p-6', className)}>
      {header}
      {(!collapsible || open) && children}
    </section>
  );
}
