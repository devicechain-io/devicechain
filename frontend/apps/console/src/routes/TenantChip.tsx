// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Building2 } from 'lucide-react';
import { useCurrentTenant } from '@/auth/TenantProvider';

// The tenant-context chip shown top-right in every tenant-app page header. Today
// it shows the tenant name + token; it is the anchor for tenant branding (a logo
// in place of the icon, brand colors) as that lands — see the branding design note.
export function TenantChip() {
  const tenant = useCurrentTenant();
  if (!tenant) return null;

  const label = tenant.name || tenant.token;
  return (
    <div className="flex items-center gap-2 rounded-md border border-border bg-card px-2.5 py-1">
      <span className="flex size-6 items-center justify-center rounded bg-primary/10 text-primary">
        <Building2 className="size-3.5" />
      </span>
      <div className="flex flex-col leading-tight">
        <span className="text-sm font-medium text-foreground">{label}</span>
        {/* Only show the token line when a name is present, so we never repeat it. */}
        {tenant.name && (
          <span className="font-mono text-[10px] text-muted-foreground">{tenant.token}</span>
        )}
      </div>
    </div>
  );
}
