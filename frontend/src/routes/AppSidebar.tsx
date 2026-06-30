// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import {
  Boxes,
  Building2,
  ChevronRight,
  Cpu,
  LayoutGrid,
  MapPin,
  Package,
  type LucideIcon,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { Logomark } from '@/components/brand/Logo';
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
import { hasAuthority, type DecodedClaims } from '@/lib/auth/jwt';
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
  {
    label: 'Devices',
    icon: Cpu,
    children: [
      // All of device-management is gated by device:read (there is no separate
      // devicetype:read), so both share the same requirement.
      { label: 'Devices', href: '/devices', icon: Cpu, requires: 'device:read' },
      { label: 'Device Types', href: '/device-types', icon: Boxes, requires: 'device:read' },
      // Device Groups land with the registry families / membership work.
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
    ],
  },
  {
    label: 'Customers',
    icon: Building2,
    children: [
      { label: 'Customers', href: '/customers', icon: Building2, requires: 'device:read' },
      { label: 'Customer Types', href: '/customer-types', icon: Boxes, requires: 'device:read' },
    ],
  },
  {
    label: 'Areas',
    icon: MapPin,
    children: [
      { label: 'Areas', href: '/areas', icon: MapPin, requires: 'device:read' },
      { label: 'Area Types', href: '/area-types', icon: Boxes, requires: 'device:read' },
    ],
  },
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
  // `open` holds the groups the user toggled open; a group is also shown expanded
  // whenever it owns the active route (so deep links / refreshes land expanded,
  // with no effect-driven state to keep in sync).
  const [open, setOpen] = useState<Set<string>>(() => new Set());

  const toggle = (label: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      next.has(label) ? next.delete(label) : next.add(label);
      return next;
    });

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild tooltip="DeviceChain">
              <Link to="/">
                <div className="flex aspect-square size-8 items-center justify-center">
                  <Logomark className="size-7" />
                </div>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold">
                    Device<span className="text-primary">Chain</span>
                  </span>
                  <span className="truncate text-xs text-muted-foreground">Management Console</span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
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
                    open={open.has(node.label) || node.label === activeGroup}
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
  const hasActiveChild = node.children.some((c) => isActive(pathname, c.href));
  return (
    <SidebarMenuItem>
      <SidebarMenuButton
        onClick={onToggle}
        // Highlight the collapsed parent so the user still sees where they are.
        isActive={hasActiveChild && !open}
        tooltip={node.label}
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
        <SidebarMenuSub>
          {node.children.map((child) => (
            <SidebarMenuSubItem key={child.href}>
              <SidebarMenuSubButton asChild isActive={isActive(pathname, child.href)}>
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