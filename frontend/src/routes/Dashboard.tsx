// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useAuth } from '@/auth/AuthProvider';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';

export default function Dashboard() {
  const { claims } = useAuth();

  return (
    <PageShell
      title={`Welcome, ${claims?.username ?? ''}`}
      description="Manage access control for your DeviceChain tenant. Device, event, and state surfaces land here as the console grows."
      bodyClassName="space-y-6 max-w-5xl"
    >
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="text-muted-foreground">Tenant</span>
        <Badge variant="secondary">{claims?.tenant || '—'}</Badge>
        <span className="ml-2 text-muted-foreground">Roles</span>
        {(claims?.roles ?? []).length > 0 ? (
          claims!.roles.map((r) => (
            <Badge key={r} variant="outline">
              {r}
            </Badge>
          ))
        ) : (
          <Badge variant="outline">none</Badge>
        )}
      </div>

    </PageShell>
  );
}