// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { ErrorBanner } from '@/components/ui/error-banner';
import { cn } from '@/lib/utils';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';

interface ErrorStateProps {
  /** Error copy shown to the user. */
  description: string;
  /** Optional dev-mode "raw response" disclosure (typically passed
   *  through from a `WireShapeError.excerpt`). Production builds drop
   *  the disclosure regardless. */
  details?: string;
  className?: string;
  onRetry?: () => void;
}

export function ErrorState({ description, details, className, onRetry }: ErrorStateProps) {
  const { t } = useTranslation('common');
  const retry = onRetry && <Button variant="outline" onClick={onRetry}>{t('retry')}</Button>;
  if (details) {
    return (
      <div className={cn('px-6 py-8 max-w-3xl mx-auto', className)} data-shot="error">
        <ErrorBanner message={description} details={details} />
        {retry}
      </div>
    );
  }
  return (
    <div role="alert" className={cn('flex flex-col gap-3 items-center justify-center h-64', className)} data-shot="error">
      <p className="text-destructive text-sm">{description}</p>
      {retry}
    </div>
  );
}
