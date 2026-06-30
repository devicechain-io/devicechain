// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Outlet, useLocation, matchPath } from 'react-router-dom';
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import { AppSidebar } from '@/routes/AppSidebar';
import { TenantChip } from '@/routes/TenantChip';
import { TenantProvider } from '@/auth/TenantProvider';
import { CurrentUserProvider } from '@/auth/CurrentUserProvider';
import { ErrorBoundary } from '@/components/ui/error-boundary';

const PAGE_TITLES: { pattern: string; title: string }[] = [
  { pattern: '/', title: 'Dashboard' },
  { pattern: '/devices', title: 'Devices' },
  { pattern: '/device-types', title: 'Device Types' },
];

export default function AppLayout() {
  const { pathname } = useLocation();
  const title =
    PAGE_TITLES.find(({ pattern }) => matchPath({ path: pattern, end: true }, pathname))?.title ??
    'DeviceChain';

  return (
    <TenantProvider>
      <CurrentUserProvider>
        <SidebarProvider>
          <AppSidebar />
          <SidebarInset className="min-w-0">
            <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-4">
              <SidebarTrigger className="-ml-1 h-9 w-9" />
              <span className="text-base font-semibold text-foreground">{title}</span>
              <div className="ml-auto">
                <TenantChip />
              </div>
            </header>
            <div className="flex min-h-0 flex-1 flex-col">
              {/* Key the boundary by route so a page crash auto-clears on navigation. */}
              <ErrorBoundary key={pathname}>
                <Outlet />
              </ErrorBoundary>
            </div>
          </SidebarInset>
        </SidebarProvider>
      </CurrentUserProvider>
    </TenantProvider>
  );
}