// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Suspense, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { LoadingState } from '@/components/ui/loading-state';

// The fallback shown while a lazily loaded route's code downloads. Placed INSIDE a layout, around
// its <Outlet />, so the sidebar and header stay put and only the page area shows the loading state.
export function RouteSuspense({ children }: { children: ReactNode }) {
  const { t } = useTranslation('common');
  return <Suspense fallback={<LoadingState description={t('loading')} />}>{children}</Suspense>;
}
