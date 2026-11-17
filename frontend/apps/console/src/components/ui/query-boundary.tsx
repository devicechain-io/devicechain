// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { QueryState } from '@/lib/hooks/use-query';
import { LoadingState } from './loading-state';
import { ErrorState } from './error-state';

/** Keep children mounted during refreshes; a different identity starts fresh. */
export function QueryBoundary<T>({ query, children }: {
  query: QueryState<T>;
  children: ReactNode;
}) {
  const { t } = useTranslation('common');
  if (query.status === 'loading') return <LoadingState description={t('loading')} />;
  if (query.status === 'error') return <ErrorState description={query.error!} onRetry={query.refetch} />;
  return <>
    {query.refreshError && <ErrorState className="h-auto py-3" description={query.refreshError} onRetry={query.refetch} />}
    {children}
  </>;
}
