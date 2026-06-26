// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Spinner } from '@/components/ui/spinner';
import { cn } from '@/lib/utils';

interface LoadingStateProps {
  /** Loading copy (defaults to "Loading..."). */
  description?: string;
  /**
   * Render a spinner above the message in a compact stacked block (the old
   * `InlineSpinner` layout). Default keeps the text-only `h-64` centered
   * block existing call sites rely on.
   */
  spinner?: boolean;
  className?: string;
}

export function LoadingState({ description, spinner = false, className }: LoadingStateProps) {
  const text = description ?? 'Loading...';
  if (spinner) {
    return (
      <div className={cn('flex flex-col items-center justify-center py-12', className)}>
        <Spinner size="md" />
        <p className="text-sm text-muted-foreground mt-3">{text}</p>
      </div>
    );
  }
  return (
    <div className={cn('flex items-center justify-center h-64', className)}>
      <p className="text-muted-foreground text-sm">{text}</p>
    </div>
  );
}