// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { ErrorBanner } from '@/components/ui/error-banner';
import { cn } from '@/lib/utils';

interface ErrorStateProps {
  /** Error copy shown to the user. */
  description: string;
  /** Optional dev-mode "raw response" disclosure (typically passed
   *  through from a `WireShapeError.excerpt`). Production builds drop
   *  the disclosure regardless. */
  details?: string;
  className?: string;
}

export function ErrorState({ description, details, className }: ErrorStateProps) {
  if (details) {
    return (
      <div className={cn('px-6 py-8 max-w-3xl mx-auto', className)}>
        <ErrorBanner message={description} details={details} />
      </div>
    );
  }
  return (
    <div className={cn('flex items-center justify-center h-64', className)}>
      <p className="text-destructive text-sm">{description}</p>
    </div>
  );
}