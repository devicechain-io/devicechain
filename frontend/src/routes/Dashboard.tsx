// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Link } from 'react-router-dom';
import { Boxes, Cpu, type LucideIcon } from 'lucide-react';
import { useCurrentUser } from '@/auth/CurrentUserProvider';
import { PageShell } from '@/components/ui/page-shell';
import { useQuery } from '@/lib/hooks/use-query';
import { listDevices, listDeviceTypes } from '@/lib/api/device-management';

// StatCard is a clickable registry summary: a count that links to its list. The
// count is null while loading and renders as a subtle dash on error.
function StatCard({
  label,
  icon: Icon,
  count,
  to,
}: {
  label: string;
  icon: LucideIcon;
  count: number | null;
  to: string;
}) {
  return (
    <Link
      to={to}
      className="flex items-center gap-4 rounded-lg border border-border bg-card p-5 transition-colors hover:border-primary/50 hover:bg-muted/40"
    >
      <div className="flex size-10 items-center justify-center rounded-md bg-primary/10 text-primary">
        <Icon size={20} />
      </div>
      <div>
        <div className="text-2xl font-semibold text-foreground">{count ?? '—'}</div>
        <div className="text-sm text-muted-foreground">{label}</div>
      </div>
    </Link>
  );
}

export default function Dashboard() {
  const user = useCurrentUser();

  // A page size of 1 keeps the count query cheap — we only read pagination.totalRecords.
  const { data: devices } = useQuery(() => listDevices({ pageNumber: 1, pageSize: 1 }), []);
  const { data: deviceTypes } = useQuery(() => listDeviceTypes({ pageNumber: 1, pageSize: 1 }), []);

  const deviceCount = devices?.pagination.totalRecords ?? null;
  const deviceTypeCount = deviceTypes?.pagination.totalRecords ?? null;

  return (
    <PageShell
      title={user ? `Welcome, ${user.displayName}` : 'Welcome'}
      description="Your DeviceChain tenant at a glance. Event and state surfaces land here as the console grows."
      bodyClassName="space-y-6 max-w-5xl"
    >
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <StatCard label="Devices" icon={Cpu} count={deviceCount} to="/devices" />
        <StatCard label="Device Types" icon={Boxes} count={deviceTypeCount} to="/device-types" />
      </div>
    </PageShell>
  );
}
