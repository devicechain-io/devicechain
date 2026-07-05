// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import {
  AlertTriangle,
  Boxes,
  Building2,
  ChevronRight,
  Cpu,
  Layers,
  LayoutDashboard,
  LayoutGrid,
  MapPin,
  Package,
  ScrollText,
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
import { hasAuthority, type DecodedClaims } from '@devicechain/client';
import { NavUser } from '@/routes/NavUser';

interface NavLeaf {
  label: string;
  href: string;
  icon: LucideIcon;
  // Authority required to see this item; omit for always-visible (e.g. Dashboard).
  requires?: string;
}

type NavGroupNode = { label: string; icon: LucideIcon; children: NavLeaf[] };

// A top-level nav node is either a direct link (Dashboard) or a collapsible
// group of leaves. Each model construct (Devices, and later Assets/Customers/
// Areas) is one group whose children are its Instances / Types / Groups — so
// adding a construct is a single config entry, not new layout code.
type NavNode = NavLeaf | NavGroupNode;

const NAV: NavNode[] = [
  { label: 'Dashboard', href: '/', icon: LayoutGrid },
  { label: 'Dashboards', href: '/dashboards', icon: LayoutDashboard, requires: 'dashboard:read' },
  { label: 'Alarms', href: '/alarms', icon: AlertTriangle, requires: 'alarm:read' },
  {
    label: 'Devices',
    icon: Cpu,
    children: [
      // All of device-management is gated by device:read (there is no separate
      // devicetype:read), so both share the same requirement.
      { label: 'Devices', href: '/devices', icon: Cpu, requires: 'device:read' },
      { label: 'Device Types', href: '/device-types', icon: Boxes, requires: 'device:read' },
      { label: 'Device Groups', href: '/device-groups', icon: Layers, requires: 'device:read' },
    ],
  },
  {
    // Asset / Customer / Area share device-management's authority model: the whole
    // service is gated by device:read, so every leaf below uses it too.
    label: 'Assets',
    icon: Package,
    children: [
      { label: 'Assets', href: '/assets', icon: Package, requires: 'device:read' },
      { label: 'Asset Types', href: '/asset-types', icon: Boxes, requires: 'device:read' },
      { label: 'Asset Groups', href: '/asset-groups', icon: Layers, requires: 'device:read' },
    ],
  },
  {
    label: 'Customers',
    icon: Building2,
    children: [
      { label: 'Customers', href: '/customers', icon: Building2, requires: 'device:read' },
      { label: 'Customer Types', href: '/customer-types', icon: Boxes, requires: 'device:read' },
      { label: 'Customer Groups', href: '/customer-groups', icon: Layers, requires: 'device:read' },
    ],
  },
  {
    label: 'Areas',
    icon: MapPin,
    children: [
      { label: 'Areas', href: '/areas', icon: MapPin, requires: 'device:read' },
      { label: 'Area Types', href: '/area-types', icon: Boxes, requires: 'device:read' },
      { label: 'Area Groups', href: '/area-groups', icon: Layers, requires: 'device:read' },
    ],
  },
  { label: 'Audit', href: '/audit', icon: ScrollText, requires: 'audit:read' },
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

// Label of the group that owns the current route, if any — used to keep the
// active group expanded (including on deep links / refreshes).
function activeGroupLabel(pathname: string): string | undefined {
  return NAV.find(
    (node) => !isLeaf(node) && node.children.some((c) => isActive(pathname, c.href)),
  )?.label;
}

export function AppSidebar() {
  const { pathname } = useLocation();
  const { claims } = useAuth();
  const nav = visibleNav(claims);
  const activeGroup = activeGroupLabel(pathname);
  // Accordion: at most one group expanded at a time, to keep the rail uncluttered.
  // Default to the group that owns the current route, and follow the route when it
  // moves into a different group (deep links / refreshes land expanded too).
  const [openGroup, setOpenGroup] = useState<string | null>(activeGroup ?? null);
  useEffect(() => {
    if (activeGroup) setOpenGroup(activeGroup);
  }, [activeGroup]);

  const toggle = (label: string) => setOpenGroup((cur) => (cur === label ? null : label));

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader className="pt-4">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild tooltip="DeviceChain" className="justify-center">
              <Link to="/">
                {/* Collapsed icon rail: cube mark only */}
                <Logomark className="hidden size-7 shrink-0 group-data-[collapsible=icon]:block" />
                {/* Expanded: horizontal lockup with the console subtitle beneath */}
                <div className="flex flex-col items-center gap-1 group-data-[collapsible=icon]:hidden">
                  <LogoHorizontal deviceColor="currentColor" className="h-[17px] w-auto" />
                  <span className="truncate text-xs text-muted-foreground">Management Console</span>
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
                  <SidebarMenuItem key={node.label}>
                    <SidebarMenuButton
                      asChild
                      isActive={isActive(pathname, node.href)}
                      tooltip={node.label}
                      className={cn(
                        'text-[0.9375rem] font-medium',
                        isActive(pathname, node.href) && '!font-semibold !text-primary',
                      )}
                    >
                      <Link to={node.href}>
                        <node.icon />
                        <span>{node.label}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ) : (
                  <NavGroup
                    key={node.label}
                    node={node}
                    pathname={pathname}
                    open={openGroup === node.label}
                    onToggle={() => toggle(node.label)}
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
        tooltip={node.label}
        className={cn(
          'text-[0.9375rem] font-medium',
          hasActiveChild && !open && '!font-semibold !text-primary',
        )}
      >
        <node.icon />
        <span>{node.label}</span>
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
                  <span>{child.label}</span>
                </Link>
              </SidebarMenuSubButton>
            </SidebarMenuSubItem>
          ))}
        </SidebarMenuSub>
      )}
    </SidebarMenuItem>
  );
}