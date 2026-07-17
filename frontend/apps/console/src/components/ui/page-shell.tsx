// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

interface PageShellProps {
  /** Page title displayed in the sticky header */
  title?: string;
  /**
   * Inline node rendered on the SAME line as the title, after it — the standard slot for
   * an entity's copyable token (see CopyToken). Kept apart from `description` (which sits
   * BELOW the title) so the token reads as part of the identity, not supporting copy.
   */
  titleAdornment?: ReactNode;
  /** Supporting copy below the title — string or ReactNode (e.g. badges) */
  description?: string | ReactNode;
  /**
   * Action buttons on the right of the header. Pass the buttons themselves — the shell
   * lays them out (right-aligned, gap between them, never shrunk by a long description).
   * Callers must NOT wrap them in their own flex row.
   */
  action?: ReactNode;
  /**
   * Category key for the muted background texture in the header, e.g. "devices".
   * Resolves to `/banners/banner-<key>-mask.png`, tinted by the foreground color
   * and laid at low opacity. Only set for top-level categories, not sub-items.
   */
  banner?: string;
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
  titleAdornment,
  description,
  action,
  header,
  banner,
  subHeader,
  fullBleed = false,
  bodyClassName,
  children,
}: PageShellProps) {
  const hasHeader = header || title;

  return (
    <div className="flex flex-col h-full">
      {hasHeader && (
        <div
          className={cn(
            'relative shrink-0 overflow-hidden border-b border-border bg-background px-6',
            // Fixed, content-independent height for the standard header so every
            // category page lines up; custom headers keep their own sizing.
            header ? 'py-4' : 'flex h-28 items-center',
          )}
        >
          {banner && (
            <>
              <div
                aria-hidden
                className="pointer-events-none absolute inset-0 bg-foreground opacity-[0.08] dark:opacity-[0.11]"
                style={{
                  WebkitMaskImage: `url(/banners/banner-${banner}-mask.png)`,
                  maskImage: `url(/banners/banner-${banner}-mask.png)`,
                  WebkitMaskSize: 'cover',
                  maskSize: 'cover',
                  WebkitMaskPosition: 'center',
                  maskPosition: 'center',
                }}
              />
              {/* Wash the texture out under the title/description on the left,
                  fading to the full pattern by the header's midpoint. Uses the
                  background color so it stays correct in both themes. */}
              <div
                aria-hidden
                className="pointer-events-none absolute inset-0"
                style={{
                  background:
                    'linear-gradient(to right, hsl(var(--background)) 0%, hsl(var(--background) / 0) 50%)',
                }}
              />
            </>
          )}
          <div className="relative w-full">
            {header ?? (
              // gap-4 keeps a long description off the actions; min-w-0 lets the left
              // column shrink/wrap instead of shoving the actions past the edge; the
              // action column is shrink-0 with its own gap so multiple buttons are spaced
              // and never crowded — the shell owns action layout, callers just pass buttons.
              <div className="flex items-center justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <h1 className="truncate text-2xl font-bold tracking-tight text-foreground">
                      {title}
                    </h1>
                    {titleAdornment}
                  </div>
                  {description && (
                    typeof description === 'string'
                      ? <p className="text-sm text-muted-foreground mt-1">{description}</p>
                      : <div className="mt-1">{description}</div>
                  )}
                </div>
                {action && <div className="flex shrink-0 items-center gap-2">{action}</div>}
              </div>
            )}
          </div>
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