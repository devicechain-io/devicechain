// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Outlet, useLocation, matchPath } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import { AdminSidebar } from '@/routes/admin/AdminSidebar';
import { ErrorBoundary } from '@/components/ui/error-boundary';
import { BackLink } from '@/routes/common';

// Each admin section maps to a `nav` catalog key (ADR-066) so the top bar localizes.
const PAGE_TITLES: { pattern: string; key: string }[] = [
  { pattern: '/admin/tenants', key: 'adminTenants' },
  { pattern: '/admin/tiers', key: 'adminTiers' },
  { pattern: '/admin/identities', key: 'adminIdentities' },
  { pattern: '/admin/roles', key: 'adminRoles' },
  { pattern: '/admin/ai-providers', key: 'adminAiProviders' },
  // Reached from the AI Providers screen (not a sidebar entry); listed so its top-bar
  // title reads "AI Packaging" rather than the generic "Admin". Not a detail route, so it
  // gets no back-link.
  { pattern: '/admin/ai-packaging', key: 'adminAiPackaging' },
  { pattern: '/admin/audit', key: 'audit' },
  { pattern: '/admin/settings', key: 'adminSettings' },
];

// The section the current admin route belongs to, and whether we're on a detail
// page under it (/admin/<section>/<id…>). Drives the top-bar title and back-link.
function adminSection(pathname: string): { key: string; base: string; isDetail: boolean } | null {
  const entry = PAGE_TITLES.find(({ pattern }) => matchPath({ path: pattern, end: false }, pathname));
  if (!entry) return null;
  const isDetail = !matchPath({ path: entry.pattern, end: true }, pathname);
  return { key: entry.key, base: entry.pattern, isDetail };
}

export default function AdminLayout() {
  const { pathname } = useLocation();
  const { t } = useTranslation('nav');
  const section = adminSection(pathname);
  // The localized section name titles the section list AND its create/detail pages
  // (the entity's own name is the big title in the page header below). Localizing the
  // English "New <Singular>" / "<Singular> Detail" affordance needs number-aware rules
  // and is deferred to sweep (b). Mirrors AppLayout.
  const title = section ? t(section.key) : t('adminBadge');

  return (
    <SidebarProvider>
      <AdminSidebar />
      <SidebarInset className="min-w-0">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-4">
          <SidebarTrigger className="-ml-1 h-9 w-9" />
          <span className="text-base font-semibold text-foreground">{title}</span>
          {/* Navigation + context indicator on the right, mirroring AppLayout's
              [back-link][tenant chip]: a detail page's back-link to its list, then the
              "Admin" badge (the admin console's context marker, playing the tenant chip's
              role). Kept out of the page header so navigation stays separate from the
              entity's own actions. */}
          <div className="ml-auto flex items-center gap-3">
            {section?.isDetail && <BackLink to={section.base}>{t(section.key)}</BackLink>}
            <span className="rounded bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
              {t('adminBadge')}
            </span>
          </div>
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
