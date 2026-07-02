// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ChevronsUpDown, LogOut, Building2 } from 'lucide-react';
import { useAuth } from '@/auth/AuthProvider';
import { ThemeToggle } from '@/components/ThemeToggle';
import { decodeToken } from '@/lib/auth/jwt';
import { useToast } from '@/components/ui/toast';
import { errMessage } from '@/routes/common';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar';

function Avatar({ name }: { name: string }) {
  const initial = name.charAt(0).toUpperCase() || '?';
  return (
    <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground text-sm font-medium">
      {initial}
    </div>
  );
}

// The admin console footer: the signed-in administrator, a "switch to tenant"
// action that exchanges the identity token for a tenant session (ADR-033), the
// theme toggle, and sign-out.
export function AdminUser() {
  const { identityToken, memberships, selectTenant, logout } = useAuth();
  const { isMobile } = useSidebar();
  const { toast } = useToast();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);

  const email = identityToken ? (decodeToken(identityToken)?.username ?? 'Administrator') : 'Administrator';

  const enterTenant = async (tenant: string) => {
    if (!identityToken) return;
    setBusy(true);
    try {
      await selectTenant(identityToken, tenant);
      navigate('/', { replace: true });
    } catch (err) {
      toast(errMessage(err), 'error');
      setBusy(false);
    }
  };

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size="lg"
              className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
            >
              <Avatar name={email} />
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-semibold">{email}</span>
                <span className="truncate text-xs text-muted-foreground">Superuser</span>
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
            <DropdownMenuLabel className="text-xs text-muted-foreground">
              Switch to tenant
            </DropdownMenuLabel>
            {memberships.length === 0 ? (
              <div className="px-2 pb-1.5 text-xs text-muted-foreground">
                You hold no tenant memberships. Add one from Identities to enter a tenant.
              </div>
            ) : (
              memberships.map((m) => (
                <DropdownMenuItem
                  key={m.tenant}
                  disabled={busy}
                  onClick={() => enterTenant(m.tenant)}
                  className="cursor-pointer"
                >
                  <Building2 size={16} />
                  {m.tenant}
                </DropdownMenuItem>
              ))
            )}

            <DropdownMenuSeparator />
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
