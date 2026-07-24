// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  Building2,
  ChevronRight,
  Layers,
  List,
  ScrollText,
  Settings,
  ShieldCheck,
  Sparkles,
  Users,
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
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarRail,
} from '@/components/ui/sidebar';
import { AdminUser } from '@/routes/admin/AdminUser';

// labelKey indexes the `nav` catalog (ADR-066), resolved with t() at render.
type NavLeaf = { labelKey: string; href: string; icon: LucideIcon };
type NavGroupNode = { labelKey: string; icon: LucideIcon; children: NavLeaf[] };
type NavNode = NavLeaf | NavGroupNode;

const isLeaf = (n: NavNode): n is NavLeaf => 'href' in n;

// The admin console manages the instance-global control plane (ADR-033): tenants and the
// packaging they are sold, the identity directory, the role catalog, and the AI provider
// list every tenant's NL authoring routes through.
//
// Tenants is a GROUP: its list and the tier catalog that prices tenants live together, so
// Tiers sits under Tenants alongside an explicit List entry rather than as its own
// top-level peer (ADR-065 — a tier is packaging OF tenants, not a separate domain).
const NAV: NavNode[] = [
  {
    labelKey: 'adminTenants',
    icon: Building2,
    children: [
      { labelKey: 'adminList', href: '/admin/tenants', icon: List },
      { labelKey: 'adminTiers', href: '/admin/tiers', icon: Layers },
    ],
  },
  {
    labelKey: 'adminIdentities',
    icon: Users,
    children: [
      { labelKey: 'adminList', href: '/admin/identities', icon: List },
      // Roles are the RBAC vocabulary identities are assigned, so the catalog nests under
      // Identities (the same "configuration OF the parent" logic that puts Tiers under
      // Tenants) rather than sitting as its own top-level peer.
      { labelKey: 'adminRoles', href: '/admin/roles', icon: ShieldCheck },
    ],
  },
  { labelKey: 'adminAiProviders', href: '/admin/ai-providers', icon: Sparkles },
  // AI packaging (which models each tier grants) is not a top-level screen: it is
  // configuration OF the tiers, reached from a tier's detail page. The route still exists
  // (a cross-tier comparison matrix); it is just not its own nav entry.
  { labelKey: 'audit', href: '/admin/audit', icon: ScrollText },
  { labelKey: 'adminSettings', href: '/admin/settings', icon: Settings },
];

// A child href matches the current route by prefix, so a tenant/tier DETAIL page keeps its
// section highlighted. The Tenants children have disjoint prefixes (/admin/tenants vs
// /admin/tiers), so at most one is ever active.
function childActive(pathname: string, href: string) {
  return pathname.startsWith(href);
}

// labelKey of the group that owns the current route, if any — used to keep the active
// group expanded (including on deep links / refreshes). Opaque accordion identity, not
// display text.
function activeGroupKey(pathname: string): string | undefined {
  return NAV.find(
    (n): n is NavGroupNode => !isLeaf(n) && n.children.some((c) => childActive(pathname, c.href)),
  )?.labelKey;
}

export function AdminSidebar() {
  const { pathname } = useLocation();
  const { t } = useTranslation('nav');
  const activeGroup = activeGroupKey(pathname);
  // Accordion: default to the group that owns the current route, and follow the route when
  // it moves into a different group (deep links / refreshes land expanded too).
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
            <SidebarMenuButton
              size="lg"
              asChild
              tooltip={t('adminTooltip')}
              className="justify-center"
            >
              <Link to="/admin">
                {/* Collapsed icon rail: cube mark only. */}
                <Logomark className="hidden size-7 shrink-0 group-data-[collapsible=icon]:block" />
                {/* Expanded: the DeviceChain lockup (same as the tenant console), with
                    the Admin Console label beneath. Admin is instance-level, so there is
                    no tenant branding here — always the DeviceChain wordmark. */}
                <div className="flex flex-col items-center gap-1 group-data-[collapsible=icon]:hidden">
                  <LogoHorizontal deviceColor="currentColor" className="h-[17px] w-auto" />
                  <span className="truncate text-xs text-muted-foreground">
                    {t('adminConsole')}
                  </span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>{t('adminPlatform')}</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {NAV.map((node) =>
                isLeaf(node) ? (
                  <SidebarMenuItem key={node.labelKey}>
                    <SidebarMenuButton
                      asChild
                      isActive={childActive(pathname, node.href)}
                      tooltip={t(node.labelKey)}
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
        <AdminUser />
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
  const hasActiveChild = node.children.some((c) => childActive(pathname, c.href));
  // Expanding a group lands you on its first item in one click (the active-group effect
  // keeps it open); clicking an already-open group just collapses it. But if the current
  // route is ALREADY inside this group (e.g. a tier detail page), expanding must not yank
  // the user back to the list — only navigate when opening from outside the group.
  const handleClick = () => {
    const willOpen = !open;
    onToggle();
    if (willOpen && !hasActiveChild) navigate(node.children[0].href);
  };
  return (
    <SidebarMenuItem>
      <SidebarMenuButton
        onClick={handleClick}
        // Highlight the collapsed parent so the user still sees where they are.
        isActive={hasActiveChild && !open}
        tooltip={t(node.labelKey)}
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
        <SidebarMenuSub>
          {node.children.map((child) => (
            <SidebarMenuSubItem key={child.href}>
              <SidebarMenuSubButton asChild isActive={childActive(pathname, child.href)}>
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
