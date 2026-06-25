// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

interface PageShellProps {
  /** Page title displayed in the sticky header */
  title?: string;
  /** Supporting copy below the title — string or ReactNode (e.g. badges) */
  description?: string | ReactNode;
  /** Action buttons on the right side of the header */
  action?: ReactNode;
  /** Fully custom header content (overrides title/description/action) */
  header?: ReactNode;
  /** Sticky strip shown below the main header — e.g. tabs + context-specific controls */
  subHeader?: ReactNode;
  /** Removes body padding and default overflow-auto; children manage their own layout */
  fullBleed?: boolean;
  /** Additional classes on the body container */
  bodyClassName?: string;
  children: ReactNode;
}

export function PageShell({
  title,
  description,
  action,
  header,
  subHeader,
  fullBleed = false,
  bodyClassName,
  children,
}: PageShellProps) {
  const hasHeader = header || title;

  return (
    <div className="flex flex-col h-full">
      {hasHeader && (
        <div className="shrink-0 border-b border-border bg-background px-6 py-4">
          {header ?? (
            <div className="flex items-center justify-between">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">{title}</h1>
                {description && (
                  typeof description === 'string'
                    ? <p className="text-sm text-muted-foreground mt-1">{description}</p>
                    : <div className="mt-1">{description}</div>
                )}
              </div>
              {action}
            </div>
          )}
        </div>
      )}
      {subHeader && (
        <div className="shrink-0 border-b border-border bg-background">
          {subHeader}
        </div>
      )}
      <div
        className={cn(
          'flex-1 min-h-0',
          fullBleed ? 'flex flex-col' : 'overflow-auto',
        )}
      >
        {fullBleed ? (
          children
        ) : (
          <div className={cn('p-6 mx-auto', bodyClassName)}>{children}</div>
        )}
      </div>
    </div>
  );
}