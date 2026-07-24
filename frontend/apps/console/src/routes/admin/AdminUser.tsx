// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ChevronsUpDown, LineChart, LogOut, Building2 } from 'lucide-react';
import { useAuth } from '@/auth/AuthProvider';
import { useMetricsAvailable } from '@/lib/hooks/use-metrics-available';
import { ThemeToggle } from '@/components/ThemeToggle';
import { LocaleSwitcher } from '@/components/LocaleSwitcher';
import { decodeToken } from '@devicechain/client';
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
  const { t } = useTranslation('userMenu');
  const { identityToken, memberships, selectTenant, logout } = useAuth();
  const { isMobile } = useSidebar();
  const { toast } = useToast();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);
  // Metrics (Grafana) is instance-level + cross-tenant, so it belongs in the
  // superuser-only admin console — and is the ONLY account menu a membership-less
  // operator ever sees (they cannot enter a tenant). The whole /admin area is already
  // superuser-gated (AdminProtectedRoute), so probe unconditionally; the hook still
  // hides the link unless Grafana SSO is actually wired.
  const metricsAvailable = useMetricsAvailable();

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
                <span className="truncate text-xs text-muted-foreground">{t('superuser')}</span>
              </div>
              <ChevronsUpDown className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            // Fixed width so the panel is sized by its controls, not by its
            // longest line of text (which localizes and would otherwise stretch
            // the menu). The no-memberships sentence wraps within this width.
            className="w-72 rounded-lg"
            side={isMobile ? 'bottom' : 'right'}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuLabel className="text-xs text-muted-foreground">
              {t('switchToTenant')}
            </DropdownMenuLabel>
            {memberships.length === 0 ? (
              <div className="px-2 pb-1.5 text-xs text-muted-foreground">
                {t('noTenantMemberships')}
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

            {metricsAvailable && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem asChild className="cursor-pointer">
                  <a href="/grafana" target="_blank" rel="noopener noreferrer">
                    <LineChart size={16} />
                    {t('metrics')}
                  </a>
                </DropdownMenuItem>
              </>
            )}

            <DropdownMenuSeparator />
            <div className="px-2 py-1.5">
              <ThemeToggle />
            </div>
            <div className="px-2 pb-1.5">
              <LocaleSwitcher className="w-full" />
            </div>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={logout} className="cursor-pointer">
              <LogOut size={16} />
              {t('signOut')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
