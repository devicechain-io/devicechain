// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Outlet, useLocation, matchPath } from 'react-router-dom';
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import { AdminSidebar } from '@/routes/admin/AdminSidebar';
import { ErrorBoundary } from '@/components/ui/error-boundary';
import { BackLink } from '@/routes/common';

const PAGE_TITLES: { pattern: string; title: string }[] = [
  { pattern: '/admin/tenants', title: 'Tenants' },
  { pattern: '/admin/tiers', title: 'Tiers' },
  { pattern: '/admin/identities', title: 'Identities' },
  { pattern: '/admin/roles', title: 'Roles' },
  { pattern: '/admin/ai-providers', title: 'AI Providers' },
  // Reached from the AI Providers screen (not a sidebar entry); listed so its top-bar
  // title reads "AI Packaging" rather than the generic "Admin". Not a detail route, so it
  // gets no back-link.
  { pattern: '/admin/ai-packaging', title: 'AI Packaging' },
  { pattern: '/admin/audit', title: 'Audit' },
  { pattern: '/admin/settings', title: 'Settings' },
];

// Singularize a section title for a detail-page label: "Tenants" -> "Tenant",
// "AI Providers" -> "AI Provider". Only the last word is touched, and the one
// irregular plural in the admin sections is handled explicitly.
function singularize(sectionTitle: string): string {
  const words = sectionTitle.split(' ');
  const last = words[words.length - 1];
  words[words.length - 1] = last === 'Identities' ? 'Identity' : last.replace(/s$/, '');
  return words.join(' ');
}

// The section the current admin route belongs to, and whether we're on a detail
// page under it (/admin/<section>/<id…>). Drives the top-bar title and back-link.
function adminSection(pathname: string): { title: string; base: string; isDetail: boolean } | null {
  const entry = PAGE_TITLES.find(({ pattern }) => matchPath({ path: pattern, end: false }, pathname));
  if (!entry) return null;
  const isDetail = !matchPath({ path: entry.pattern, end: true }, pathname);
  return { title: entry.title, base: entry.pattern, isDetail };
}

export default function AdminLayout() {
  const { pathname } = useLocation();
  const section = adminSection(pathname);
  const isCreate = pathname.endsWith('/new');
  // List page reads as the section; a create page as "New <Singular>"; any other detail
  // page as "<Singular> Detail" (the entity's own name is the big title in the page header
  // below). Mirrors AppLayout.
  const title = !section
    ? 'Admin'
    : isCreate
      ? `New ${singularize(section.title)}`
      : section.isDetail
        ? `${singularize(section.title)} Detail`
        : section.title;

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
            {section?.isDetail && <BackLink to={section.base}>{section.title}</BackLink>}
            <span className="rounded bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
              Admin
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
