// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Outlet, useLocation } from 'react-router-dom';
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import { AppSidebar } from '@/routes/AppSidebar';
import { TenantChip } from '@/routes/TenantChip';
import { TenantProvider } from '@/auth/TenantProvider';
import { CurrentUserProvider } from '@/auth/CurrentUserProvider';
import { ErrorBoundary } from '@/components/ui/error-boundary';
import { BackLink } from '@/routes/common';

// Title-case a route segment: "device-types" -> "Device Types".
function humanize(segment: string): string {
  return segment
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ');
}

// The active page's name, derived from the path so it stays correct without a
// per-route registry: list pages read as their (plural) segment; detail pages
// singularize it and append "Detail".
//   /                -> Dashboard
//   /devices         -> Devices
//   /devices/:token  -> Device Detail
//   /device-types/:t -> Device Type Detail
function pageTitle(pathname: string): string {
  const segments = pathname.split('/').filter(Boolean);
  if (segments.length === 0) return 'Dashboard';
  const list = humanize(segments[0]);
  if (segments.length === 1) return list;
  const words = list.split(' ');
  words[words.length - 1] = words[words.length - 1].replace(/s$/, '');
  return `${words.join(' ')} Detail`;
}

// On a detail page, the header carries a back-link to its list (navigation kept
// separate from the page content's own actions).
function detailBack(pathname: string): { to: string; label: string } | null {
  const segments = pathname.split('/').filter(Boolean);
  if (segments.length < 2) return null;
  return { to: `/${segments[0]}`, label: humanize(segments[0]) };
}

export default function AppLayout() {
  const { pathname } = useLocation();
  const title = pageTitle(pathname);
  const back = detailBack(pathname);

  return (
    <TenantProvider>
      <CurrentUserProvider>
        <SidebarProvider>
          <AppSidebar />
          <SidebarInset className="min-w-0">
            <header className="flex h-14 shrink-0 items-center gap-1 border-b border-border px-4">
              <SidebarTrigger className="-ml-1 h-9 w-9" />
              <span className="text-base font-semibold text-foreground">{title}</span>
              <div className="ml-auto flex items-center gap-3">
                {back && <BackLink to={back.to}>{back.label}</BackLink>}
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
