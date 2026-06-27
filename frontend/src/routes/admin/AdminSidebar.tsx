// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Link, useLocation } from 'react-router-dom';
import { Building2, ShieldCheck, Users } from 'lucide-react';
import { BrandMark } from '@/components/BrandMark';
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
// tenants, the identity directory, and the role catalog.
const NAV = [
  { label: 'Tenants', href: '/admin/tenants', icon: Building2 },
  { label: 'Identities', href: '/admin/identities', icon: Users },
  { label: 'Roles', href: '/admin/roles', icon: ShieldCheck },
];

export function AdminSidebar() {
  const { pathname } = useLocation();

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild tooltip="DeviceChain Admin">
              <Link to="/admin">
                <div className="flex aspect-square size-8 items-center justify-center">
                  <BrandMark className="size-7" />
                </div>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold">
                    Device<span className="text-primary">Chain</span>
                  </span>
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
