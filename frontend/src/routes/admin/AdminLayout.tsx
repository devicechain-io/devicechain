// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Outlet, useLocation, matchPath } from 'react-router-dom';
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import { AdminSidebar } from '@/routes/admin/AdminSidebar';
import { ErrorBoundary } from '@/components/ui/error-boundary';

const PAGE_TITLES: { pattern: string; title: string }[] = [
  { pattern: '/admin/tenants', title: 'Tenants' },
  { pattern: '/admin/identities', title: 'Identities' },
  { pattern: '/admin/roles', title: 'Roles' },
];

export default function AdminLayout() {
  const { pathname } = useLocation();
  const title =
    PAGE_TITLES.find(({ pattern }) => matchPath({ path: pattern, end: false }, pathname))?.title ??
    'Admin';

  return (
    <SidebarProvider>
      <AdminSidebar />
      <SidebarInset className="min-w-0">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-4">
          <SidebarTrigger className="-ml-1 h-9 w-9" />
          <span className="text-base font-semibold text-foreground">{title}</span>
          <span className="ml-2 rounded bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
            Admin
          </span>
        </header>
        <div className="app-gradient flex min-h-0 flex-1 flex-col">
          <ErrorBoundary key={pathname}>
            <Outlet />
          </ErrorBoundary>
        </div>
      </SidebarInset>
    </SidebarProvider>
  );
}
