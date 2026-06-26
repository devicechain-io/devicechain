// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { ChevronsUpDown, LogOut } from 'lucide-react';
import { useAuth } from '@/auth/AuthProvider';
import { ThemeToggle } from '@/components/ThemeToggle';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar';

function UserAvatar({ name }: { name: string }) {
  const initial = name.charAt(0).toUpperCase() || '?';
  return (
    <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground text-sm font-medium">
      {initial}
    </div>
  );
}

export function NavUser() {
  const { claims, logout } = useAuth();
  const { isMobile } = useSidebar();

  if (!claims) return null;

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size="lg"
              className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
            >
              <UserAvatar name={claims.username} />
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-semibold">{claims.username}</span>
                <span className="truncate text-xs text-muted-foreground">{claims.tenant}</span>
              </div>
              <ChevronsUpDown className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-[--radix-dropdown-menu-trigger-width] min-w-56 rounded-lg"
            side={isMobile ? 'bottom' : 'right'}
            align="end"
            sideOffset={4}
          >
            <div className="px-2 py-2">
              <p className="text-sm font-medium text-foreground">{claims.username}</p>
              <p className="text-xs text-muted-foreground">
                Tenant: {claims.tenant || '—'}
              </p>
            </div>

            <div className="px-2 py-1.5">
              <ThemeToggle />
            </div>

            <DropdownMenuSeparator />

            <DropdownMenuItem onClick={logout} className="cursor-pointer">
              <LogOut size={16} />
              Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}