// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Building2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { scopeAnchorRefs } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { useCurrentTenant } from '@/auth/TenantProvider';
import { useBrandingLogoSrc } from '@/lib/useBrandingLogo';
import { useCustomerNames } from '@/routes/members/ScopeBadges';

// The tenant-context chip shown top-right in every tenant-app page header. Shows
// the tenant name + token, and the tenant's branding logo in place of the generic
// icon when set (ADR-038). The logo is rendered only in an <img> (never inlined),
// so an SVG-via-https logo cannot execute script; an object-store logo (ADR-058)
// is fetched with the caller's token by useBrandingLogoSrc.
//
// For a member limited to part of the tenant (a customer user) the second line names what
// they are limited to and the role they hold — "Acme · viewer" — so a person switching
// between a full membership and a limited one can see which they are in.
export function TenantChip() {
  const tenant = useCurrentTenant();
  const logo = useBrandingLogoSrc(tenant?.branding?.logo);
  const { t } = useTranslation('members');
  const { claims } = useAuth();
  const anchors = scopeAnchorRefs(claims).filter((a) => a.kind === 'customer').map((a) => a.token);
  const names = useCustomerNames(anchors);
  if (!tenant) return null;
  const customers = anchors.map((a) => names[a] ?? a).join(', ');
  const roles = (claims?.roles ?? []).join(', ');
  const scopeLine = anchors.length === 0 ? null : roles ? t('chipScope', { customers, roles }) : customers;

  const label = tenant.name || tenant.token;
  return (
    <div data-shot="tenant-chip" className="flex items-center gap-2 rounded-md border border-border bg-card px-2.5 py-1">
      {logo ? (
        <img src={logo} alt="" className="size-6 rounded object-contain" />
      ) : (
        <span className="flex size-6 items-center justify-center rounded bg-primary/10 text-primary">
          <Building2 className="size-3.5" />
        </span>
      )}
      <div className="flex flex-col leading-tight">
        <span className="text-sm font-medium text-foreground">{label}</span>
        {scopeLine && <span className="text-[11px] text-muted-foreground" data-shot="tenant-chip-scope">{scopeLine}</span>}
        {/* Only show the token line when a name is present, so we never repeat it. */}
        {!scopeLine && tenant.name && (
          <span className="font-mono text-[10px] text-muted-foreground">{tenant.token}</span>
        )}
      </div>
    </div>
  );
}
