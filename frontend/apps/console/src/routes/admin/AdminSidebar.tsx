// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Link, useLocation } from 'react-router-dom';
import {
  Building2,
  Layers,
  ScrollText,
  Settings,
  ShieldCheck,
  Sparkles,
  Users,
} from 'lucide-react';
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
  SidebarRail,
} from '@/components/ui/sidebar';
import { AdminUser } from '@/routes/admin/AdminUser';

// The admin console manages the instance-global control plane (ADR-033):
// tenants and the packaging they are sold, the identity directory, the role
// catalog, and the AI provider list every tenant's NL authoring routes through.
const NAV = [
  { label: 'Tenants', href: '/admin/tenants', icon: Building2 },
  { label: 'Tiers', href: '/admin/tiers', icon: Layers },
  { label: 'Identities', href: '/admin/identities', icon: Users },
  { label: 'Roles', href: '/admin/roles', icon: ShieldCheck },
  { label: 'AI Providers', href: '/admin/ai-providers', icon: Sparkles },
  // AI packaging (which models each tier grants) is not a top-level screen: it is
  // configuration OF the tiers, reached from a tier's detail page. The route still exists
  // (a cross-tier comparison matrix); it is just not its own nav entry.
  { label: 'Audit', href: '/admin/audit', icon: ScrollText },
  { label: 'Settings', href: '/admin/settings', icon: Settings },
];

export function AdminSidebar() {
  const { pathname } = useLocation();

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader className="pt-4">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              size="lg"
              asChild
              tooltip="DeviceChain Admin"
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
                  <span className="truncate text-xs text-muted-foreground">Admin Console</span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Platform</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {NAV.map((item) => (
                <SidebarMenuItem key={item.href}>
                  <SidebarMenuButton
                    asChild
                    isActive={pathname.startsWith(item.href)}
                    tooltip={item.label}
                  >
                    <Link to={item.href}>
                      <item.icon />
                      <span>{item.label}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
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
