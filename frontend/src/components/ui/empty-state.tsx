// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';

interface EmptyStateProps {
  /** Supporting copy explaining the empty state. */
  description: string;
  /** Optional CTA — typically a `<Link>` or `<button>` styled with the
   *  shared `EMPTY_STATE_ACTION_CLASS` so the component stays
   *  router-agnostic. */
  action?: ReactNode;
}

/** Shared className for action buttons/links inside `<EmptyState>`. */
export const EMPTY_STATE_ACTION_CLASS =
  'text-primary hover:text-primary/80 text-sm mt-2 inline-block transition-colors';

export function EmptyState({ description, action }: EmptyStateProps) {
  return (
    <div className="text-center py-12">
      <p className="text-muted-foreground text-sm">{description}</p>
      {action}
    </div>
  );
}