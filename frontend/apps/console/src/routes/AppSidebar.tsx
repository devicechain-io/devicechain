// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  AlertTriangle,
  Boxes,
  Building2,
  ChevronRight,
  Cpu,
  Filter,
  Layers,
  LayoutDashboard,
  LayoutGrid,
  MapPin,
  Package,
  Palette,
  ScrollText,
  SlidersHorizontal,
  Tags,
  Webhook,
  type LucideIcon,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { Logomark, LogoHorizontal } from '@/components/brand/Logo';
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarRail,
} from '@/components/ui/sidebar';
import { useAuth } from '@/auth/AuthProvider';
import { useCurrentTenant } from '@/auth/TenantProvider';
import { useBrandingLogoSrc } from '@/lib/useBrandingLogo';
import { hasAuthority, type DecodedClaims } from '@devicechain/client';
import { NavUser } from '@/routes/NavUser';

interface NavLeaf {
  // A key into the `nav` catalog (ADR-066), resolved with t() at render — never a
  // literal display string, so the sidebar localizes.
  labelKey: string;
  href: string;
  icon: LucideIcon;
  // Authority required to see this item; omit for always-visible (e.g. Dashboard).
  requires?: string;
}

type NavGroupNode = { labelKey: string; icon: LucideIcon; children: NavLeaf[] };

// A top-level nav node is either a direct link (Dashboard) or a collapsible
// group of leaves. Each model construct (Devices, and later Assets/Customers/
// Areas) is one group whose children are its Instances / Types / Groups — so
// adding a construct is a single config entry, not new layout code.
type NavNode = NavLeaf | NavGroupNode;

const NAV: NavNode[] = [
  { labelKey: 'dashboard', href: '/', icon: LayoutGrid },
  { labelKey: 'dashboards', href: '/dashboards', icon: LayoutDashboard, requires: 'dashboard:read' },
  { labelKey: 'alarms', href: '/alarms', icon: AlertTriangle, requires: 'alarm:read' },
  { labelKey: 'connectors', href: '/connectors', icon: Webhook, requires: 'connector:read' },
  {
    labelKey: 'devices',
    icon: Cpu,
    children: [
      // All of device-management is gated by device:read (there is no separate
      // devicetype:read), so both share the same requirement.
      { labelKey: 'devices', href: '/devices', icon: Cpu, requires: 'device:read' },
      { labelKey: 'deviceTypes', href: '/device-types', icon: Boxes, requires: 'device:read' },
      { labelKey: 'deviceProfiles', href: '/device-profiles', icon: SlidersHorizontal, requires: 'device:read' },
      { labelKey: 'deviceGroups', href: '/device-groups', icon: Layers, requires: 'device:read' },
    ],
  },
  {
    // Asset / Customer / Area share device-management's authority model: the whole
    // service is gated by device:read, so every leaf below uses it too.
    labelKey: 'assets',
    icon: Package,
    children: [
      { labelKey: 'assets', href: '/assets', icon: Package, requires: 'device:read' },
      { labelKey: 'assetTypes', href: '/asset-types', icon: Boxes, requires: 'device:read' },
      { labelKey: 'assetGroups', href: '/asset-groups', icon: Layers, requires: 'device:read' },
    ],
  },
  {
    labelKey: 'customers',
    icon: Building2,
    children: [
      { labelKey: 'customers', href: '/customers', icon: Building2, requires: 'device:read' },
      { labelKey: 'customerTypes', href: '/customer-types', icon: Boxes, requires: 'device:read' },
      { labelKey: 'customerGroups', href: '/customer-groups', icon: Layers, requires: 'device:read' },
    ],
  },
  {
    labelKey: 'areas',
    icon: MapPin,
    children: [
      { labelKey: 'areas', href: '/areas', icon: MapPin, requires: 'device:read' },
      { labelKey: 'areaTypes', href: '/area-types', icon: Boxes, requires: 'device:read' },
      { labelKey: 'areaGroups', href: '/area-groups', icon: Layers, requires: 'device:read' },
    ],
  },
  // Facets classify every member family (ADR-061), so they are one cross-cutting
  // registry rather than a leaf under each construct group. Gated by device:read
  // like the rest of device-management.
  { labelKey: 'facets', href: '/facets', icon: Tags, requires: 'device:read' },
  // Faceted browse + dynamic groups (ADR-061 G4) — compose a selector from facet
  // axes, preview matches live, save it as a dynamic group. Cross-cutting like Facets.
  { labelKey: 'browse', href: '/browse', icon: Filter, requires: 'device:read' },
  { labelKey: 'audit', href: '/audit', icon: ScrollText, requires: 'audit:read' },
  { labelKey: 'branding', href: '/branding', icon: Palette, requires: 'branding:write' },
  // Inference providers for NL→rule authoring (ADR-056) are NOT here: they are
  // instance config an operator owns, so they live in the admin console (ADR-065). A
  // tenant's only say over AI is its external-routing consent, set per tenant by an
  // operator.
];

function isLeaf(node: NavNode): node is NavLeaf {
  return 'href' in node;
}

function canSee(leaf: NavLeaf, claims: DecodedClaims | null): boolean {
  return !leaf.requires || hasAuthority(claims, leaf.requires);
}

// Drop nav the user can't use: leaves they lack the authority for, and any group
// left with no visible children. Pages stay fail-closed server-side regardless;
// this just avoids advertising what would only return "forbidden".
function visibleNav(claims: DecodedClaims | null): NavNode[] {
  return NAV.flatMap<NavNode>((node) => {
    if (isLeaf(node)) return canSee(node, claims) ? [node] : [];
    const children = node.children.filter((c) => canSee(c, claims));
    return children.length > 0 ? [{ ...node, children }] : [];
  });
}

function isActive(pathname: string, href: string) {
  return href === '/' ? pathname === '/' : pathname.startsWith(href);
}

// labelKey of the group that owns the current route, if any — used to keep the
// active group expanded (including on deep links / refreshes). The key is an
// opaque accordion identity here, not display text.
function activeGroupKey(pathname: string): string | undefined {
  return NAV.find(
    (node) => !isLeaf(node) && node.children.some((c) => isActive(pathname, c.href)),
  )?.labelKey;
}

export function AppSidebar() {
  const { pathname } = useLocation();
  const { t } = useTranslation('nav');
  const { claims } = useAuth();
  const tenant = useCurrentTenant();
  const brandLogo = useBrandingLogoSrc(tenant?.branding?.logo);
  const brandHeight = tenant?.branding?.logoMaxHeight ?? 24;
  const nav = visibleNav(claims);
  const activeGroup = activeGroupKey(pathname);
  // Accordion: at most one group expanded at a time, to keep the rail uncluttered.
  // Default to the group that owns the current route, and follow the route when it
  // moves into a different group (deep links / refreshes land expanded too).
  const [openGroup, setOpenGroup] = useState<string | null>(activeGroup ?? null);
  useEffect(() => {
    if (activeGroup) setOpenGroup(activeGroup);
  }, [activeGroup]);

  const toggle = (key: string) => setOpenGroup((cur) => (cur === key ? null : key));

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader className="pt-4">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild tooltip="DeviceChain" className="justify-center">
              <Link to="/">
                {/* Collapsed icon rail: cube mark only (a tenant favicon/mark is
                    Phase 3; the rail keeps the DeviceChain cube for now). */}
                <Logomark className="hidden size-7 shrink-0 group-data-[collapsible=icon]:block" />
                {/* Expanded: the tenant's branding logo when set (ADR-038), else
                    the DeviceChain lockup. Rendered only in an <img> so an
                    SVG-via-https logo cannot execute script. */}
                <div className="flex flex-col items-center gap-1 group-data-[collapsible=icon]:hidden">
                  {brandLogo ? (
                    <img
                      src={brandLogo}
                      alt={tenant?.name || tenant?.token || t('common:tenantFallback')}
                      className="w-auto max-w-full object-contain"
                      style={{ maxHeight: brandHeight }}
                    />
                  ) : (
                    <LogoHorizontal deviceColor="currentColor" className="h-[17px] w-auto" />
                  )}
                  <span className="truncate text-xs text-muted-foreground">
                    {t('managementConsole')}
                  </span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent className="overflow-x-hidden">
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {nav.map((node) =>
                isLeaf(node) ? (
                  <SidebarMenuItem key={node.labelKey}>
                    <SidebarMenuButton
                      asChild
                      isActive={isActive(pathname, node.href)}
                      tooltip={t(node.labelKey)}
                      className={cn(
                        'text-[0.9375rem] font-medium',
                        isActive(pathname, node.href) && '!font-semibold !text-primary',
                      )}
                    >
                      <Link to={node.href}>
                        <node.icon />
                        <span>{t(node.labelKey)}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ) : (
                  <NavGroup
                    key={node.labelKey}
                    node={node}
                    pathname={pathname}
                    open={openGroup === node.labelKey}
                    onToggle={() => toggle(node.labelKey)}
                  />
                ),
              )}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        <NavUser />
      </SidebarFooter>

      <SidebarRail />
    </Sidebar>
  );
}

function NavGroup({
  node,
  pathname,
  open,
  onToggle,
}: {
  node: NavGroupNode;
  pathname: string;
  open: boolean;
  onToggle: () => void;
}) {
  const navigate = useNavigate();
  const { t } = useTranslation('nav');
  const hasActiveChild = node.children.some((c) => isActive(pathname, c.href));
  // Expanding a category lands you on its first item in one click (the active-group
  // effect keeps it open); clicking an already-open group just collapses it.
  const handleClick = () => {
    const willOpen = !open;
    onToggle();
    if (willOpen) navigate(node.children[0].href);
  };
  return (
    <SidebarMenuItem>
      <SidebarMenuButton
        onClick={handleClick}
        // Highlight the collapsed parent so the user still sees where they are.
        isActive={hasActiveChild && !open}
        tooltip={t(node.labelKey)}
        className={cn(
          'text-[0.9375rem] font-medium',
          hasActiveChild && !open && '!font-semibold !text-primary',
        )}
      >
        <node.icon />
        <span>{t(node.labelKey)}</span>
        <ChevronRight
          className={cn(
            'ml-auto transition-transform group-data-[collapsible=icon]:hidden',
            open && 'rotate-90',
          )}
        />
      </SidebarMenuButton>
      {open && (
        <SidebarMenuSub className="-mx-2 rounded-none border-l-0 bg-sidebar-accent/70 py-2.5 pl-6 pr-2">
          {node.children.map((child) => (
            <SidebarMenuSubItem key={child.href}>
              <SidebarMenuSubButton
                asChild
                isActive={isActive(pathname, child.href)}
                className={cn(
                  // Squared, edge-to-edge rows; the active item is shown by font/color
                  // alone (no fill) so it reads clean against the lighter panel.
                  'rounded-none text-[0.8125rem] data-[active=true]:bg-transparent',
                  isActive(pathname, child.href) && '!font-semibold !text-primary',
                )}
              >
                <Link to={child.href}>
                  <child.icon />
                  <span>{t(child.labelKey)}</span>
                </Link>
              </SidebarMenuSubButton>
            </SidebarMenuSubItem>
          ))}
        </SidebarMenuSub>
      )}
    </SidebarMenuItem>
  );
}