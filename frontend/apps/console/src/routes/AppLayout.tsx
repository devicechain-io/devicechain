// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Outlet, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import { AppSidebar } from '@/routes/AppSidebar';
import { TenantChip } from '@/routes/TenantChip';
import { TenantProvider } from '@/auth/TenantProvider';
import { CurrentUserProvider } from '@/auth/CurrentUserProvider';
import { ErrorBoundary } from '@/components/ui/error-boundary';
import { BackLink } from '@/routes/common';

// Title-case a route segment: "device-types" -> "Device Types". Fallback for a
// route not in NAV_SECTION_KEY below (a new page whose nav catalog entry hasn't
// been added yet) — it keeps such a page's top bar readable in English rather than
// blank while (b)'s sweep catches up.
function humanize(segment: string): string {
  return segment
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ');
}

// First-path-segment -> `nav` catalog key, so the top-bar title and back-link
// localize (ADR-066). A section's list AND its detail pages both read as the
// localized section name; the entity's own name is the big title in the page body.
// Fully localizing the English "<Singular> Detail" affordance needs number-aware
// rules and is deferred to the sweep (b) — until then a detail page's top bar shows
// the section name, not "<Thing> Detail".
const NAV_SECTION_KEY: Record<string, string> = {
  '': 'dashboard',
  devices: 'devices',
  'device-types': 'deviceTypes',
  'device-profiles': 'deviceProfiles',
  'device-groups': 'deviceGroups',
  dashboards: 'dashboards',
  alarms: 'alarms',
  connectors: 'connectors',
  assets: 'assets',
  'asset-types': 'assetTypes',
  'asset-groups': 'assetGroups',
  customers: 'customers',
  'customer-types': 'customerTypes',
  'customer-groups': 'customerGroups',
  areas: 'areas',
  'area-types': 'areaTypes',
  'area-groups': 'areaGroups',
  facets: 'facets',
  browse: 'browse',
  audit: 'audit',
  branding: 'branding',
};

export default function AppLayout() {
  const { pathname } = useLocation();
  const { t } = useTranslation('nav');
  const segments = pathname.split('/').filter(Boolean);
  const seg0 = segments[0] ?? '';
  const navKey = NAV_SECTION_KEY[seg0];
  const sectionLabel = navKey ? t(navKey) : humanize(seg0);
  const title = seg0 === '' ? t('dashboard') : sectionLabel;
  // On a detail page (>= 2 segments) the header carries a back-link to its list,
  // labeled with the localized section name (navigation kept separate from the
  // page content's own actions).
  const back = segments.length >= 2 ? { to: `/${seg0}`, label: sectionLabel } : null;

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
            <div className="app-gradient flex min-h-0 flex-1 flex-col">
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
