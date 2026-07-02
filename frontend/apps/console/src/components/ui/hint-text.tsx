// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

interface HintTextProps {
  children: ReactNode;
  /** `sm` → 10px (`text-label`), `md` → 11px (`text-label-lg`). */
  size?: 'sm' | 'md';
  className?: string;
}

/**
 * Muted helper/hint paragraph — the small explanatory copy under controls in
 * drawers and admin forms. Canonical replacement for the ad-hoc
 * `text-[10px]/[11px]/xs text-muted-foreground(/70|/80)` paragraphs
 * (UI-consistency backlog #5).
 */
export function HintText({ children, size = 'sm', className }: HintTextProps) {
  return (
    <p
      className={cn(
        size === 'sm' ? 'text-label' : 'text-label-lg',
        'text-muted-foreground/80',
        className,
      )}
    >
      {children}
    </p>
  );
}