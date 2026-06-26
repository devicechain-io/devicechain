// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Link } from 'react-router-dom';
import { ShieldCheck, Users, ArrowRight } from 'lucide-react';
import { useAuth } from '@/auth/AuthProvider';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';

function NavCard({
  to,
  title,
  description,
  icon: Icon,
}: {
  to: string;
  title: string;
  description: string;
  icon: typeof Users;
}) {
  return (
    <Link
      to={to}
      className="group flex items-start gap-4 rounded-lg border border-border bg-card p-5 transition-colors hover:border-primary/40 hover:bg-accent/40"
    >
      <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <Icon className="size-5" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5 font-medium text-foreground">
          {title}
          <ArrowRight className="size-3.5 opacity-0 transition-opacity group-hover:opacity-100" />
        </div>
        <p className="mt-1 text-sm text-muted-foreground">{description}</p>
      </div>
    </Link>
  );
}

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

      <div className="grid gap-4 sm:grid-cols-2">
        <NavCard
          to="/users"
          title="Users"
          description="View the people in your tenant and their assigned roles."
          icon={Users}
        />
        <NavCard
          to="/roles"
          title="Roles"
          description="Inspect roles and the capabilities (authorities) they grant."
          icon={ShieldCheck}
        />
      </div>
    </PageShell>
  );
}