// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { ChevronsUpDown, LineChart, LogOut, ShieldCheck, UserPen } from 'lucide-react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/AuthProvider';
import { useCurrentUser } from '@/auth/CurrentUserProvider';
import { useMetricsAvailable } from '@/lib/hooks/use-metrics-available';
import { ThemeToggle } from '@/components/ThemeToggle';
import { LocaleSwitcher } from '@/components/LocaleSwitcher';
import { FormDrawer } from '@/components/registry';
import { ProfileForm } from '@/routes/ProfileForm';
import { useToast } from '@/components/ui/toast';
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
  const { t } = useTranslation('userMenu');
  const { claims, logout, superuser, isIdentityAuthenticated } = useAuth();
  const user = useCurrentUser();
  const { isMobile } = useSidebar();
  const { toast } = useToast();
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  // Metrics (Grafana) is instance-level + cross-tenant, so the link is operator-only.
  // Only probe (and only ever show it) for an authenticated superuser.
  const metricsAvailable = useMetricsAvailable(superuser && isIdentityAuthenticated);

  if (!claims) return null;

  // Prefer the human name; fall back to the email (the token subject) until the
  // identity record loads.
  const name = user?.displayName ?? claims.username;
  const email = user?.email ?? claims.username;

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size="lg"
              className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
            >
              <UserAvatar name={name} />
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-semibold">{name}</span>
                <span className="truncate text-xs text-muted-foreground">{email}</span>
              </div>
              <ChevronsUpDown className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            // Fixed width so the panel is sized by its controls, not by its
            // longest line of text (which localizes and would otherwise stretch
            // the menu). Text inside wraps within this width.
            className="w-72 rounded-lg"
            side={isMobile ? 'bottom' : 'right'}
            align="end"
            sideOffset={4}
          >
            <div className="px-2 py-2">
              <p className="break-words text-sm font-medium text-foreground">{name}</p>
              <p className="break-words text-xs text-muted-foreground">{email}</p>
            </div>

            <div className="px-2 py-1.5">
              <ThemeToggle />
            </div>
            <div className="px-2 pb-1.5">
              <LocaleSwitcher className="w-full" />
            </div>

            <DropdownMenuSeparator />

            <DropdownMenuItem onClick={() => setEditing(true)} className="cursor-pointer">
              <UserPen size={16} />
              {t('editProfile')}
            </DropdownMenuItem>

            {superuser && isIdentityAuthenticated && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={() => navigate('/admin')}
                  className="cursor-pointer"
                >
                  <ShieldCheck size={16} />
                  {t('adminConsole')}
                </DropdownMenuItem>
                {metricsAvailable && (
                  <DropdownMenuItem asChild className="cursor-pointer">
                    <a href="/grafana" target="_blank" rel="noopener noreferrer">
                      <LineChart size={16} />
                      {t('metrics')}
                    </a>
                  </DropdownMenuItem>
                )}
              </>
            )}

            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={logout} className="cursor-pointer">
              <LogOut size={16} />
              {t('signOut')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <FormDrawer
          open={editing}
          onOpenChange={setEditing}
          title={t('editProfile')}
          description={t('editProfileDescription')}
        >
          <ProfileForm
            onDone={(message) => {
              toast(message);
              setEditing(false);
            }}
          />
        </FormDrawer>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}