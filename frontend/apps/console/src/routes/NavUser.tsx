// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { ChevronsUpDown, LineChart, Link2, LockKeyhole, LogOut, ShieldCheck, UserPen } from 'lucide-react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/AuthProvider';
import { useCurrentUser } from '@/auth/CurrentUserProvider';
import { useMetricsAvailable } from '@/lib/hooks/use-metrics-available';
import { ThemeToggle } from '@/components/ThemeToggle';
import { LocaleSwitcher } from '@/components/LocaleSwitcher';
import { FormDrawer } from '@/components/registry';
import { ProfileForm } from '@/routes/ProfileForm';
import { LinkSsoAccount } from '@/routes/LinkSsoAccount';
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
  const { claims, logout, superuser, isIdentityAuthenticated, signInMethod } = useAuth();
  const user = useCurrentUser();
  const { isMobile } = useSidebar();
  const { toast } = useToast();
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  const [linking, setLinking] = useState(false);
  // Linking needs the identity token of a PASSWORD sign-in. A session known to have come
  // through a provider is not offered it (the server refuses one), nor is a superuser (an
  // identity with a system role is linked only by a superuser). Every other session is: when
  // its password sign-in has lapsed, or its method is unknown, the drawer says to sign in
  // again instead of the action disappearing without a word.
  const canLinkSso = signInMethod !== 'sso' && !superuser;
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
              data-shot="user-menu"
              size="lg"
              className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
            >
              <UserAvatar name={name} />
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-semibold">{name}</span>
                <span className="truncate text-xs text-muted-foreground" data-shot-pii>{email}</span>
              </div>
              <ChevronsUpDown className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            // Fixed width so the panel is sized by its controls, not by its
            // longest line of text (which localizes and would otherwise stretch
            // the menu). Text inside wraps within this width.
            data-shot="user-menu-content"
            className="w-72 rounded-lg"
            side={isMobile ? 'bottom' : 'right'}
            align="end"
            sideOffset={4}
          >
            <div className="px-2 py-2">
              <p className="break-words text-sm font-medium text-foreground">{name}</p>
              <p className="break-words text-xs text-muted-foreground" data-shot-pii>{email}</p>
            </div>

            <div className="px-2 py-1.5">
              <ThemeToggle />
            </div>
            <div className="px-2 pb-1.5">
              <LocaleSwitcher className="w-full" />
            </div>

            <DropdownMenuSeparator />

            <DropdownMenuItem onClick={() => setEditing(true)} className="cursor-pointer" data-shot="user-menu-profile">
              <UserPen size={16} />
              {t('editProfile')}
            </DropdownMenuItem>

            {/* Two-factor authentication guards a PASSWORD sign-in: a session that came through
                a provider is governed by the provider's own requirements instead. */}
            {signInMethod !== 'sso' && (
              <DropdownMenuItem onClick={() => navigate('/security')} className="cursor-pointer" data-shot="user-menu-security">
                <LockKeyhole size={16} />
                {t('security')}
              </DropdownMenuItem>
            )}

            {canLinkSso && (
              <DropdownMenuItem onClick={() => setLinking(true)} className="cursor-pointer">
                <Link2 size={16} />
                {t('linkSso')}
              </DropdownMenuItem>
            )}

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
          shot="edit-profile"
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

        {canLinkSso && (
          <FormDrawer
            shot="link-sso-account"
            open={linking}
            onOpenChange={setLinking}
            title={t('linkSso')}
            description={t('linkSsoDescription')}
          >
            <LinkSsoAccount />
          </FormDrawer>
        )}
      </SidebarMenuItem>
    </SidebarMenu>
  );
}