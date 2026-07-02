// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2, Wifi, WifiOff } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { TypeCapsule, TokenCapsule, appearanceOf } from '@/components/TypeCapsule';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { getDevice, deleteDevice } from '@/lib/api/device-management';
import { getDeviceState, getLatestMeasurements } from '@/lib/api/device-state';
import { listEvents } from '@/lib/api/event-management';
import { formatTime } from '@/lib/utils';
import { errMessage, useReload } from '@/routes/common';
import { DeviceForm } from '@/routes/devices/DeviceForm';
import { DeviceCommandsPanel } from '@/routes/devices/DeviceCommandsPanel';
import { DeviceCredentialsPanel } from '@/routes/devices/DeviceCredentialsPanel';
import { DeviceAssignmentPanel } from '@/routes/devices/DeviceAssignmentPanel';

export default function DeviceDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();

  const [version, reload] = useReload();
  const { data: device, loading, error } = useQuery(() => getDevice(token), [version]);

  if (loading) {
    return (
      <PageShell title={token} banner="devices">
        <LoadingState description="Loading device…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!device) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={`Device “${token}” not found.`} />
      </PageShell>
    );
  }

  const remove = async () => {
    if (!window.confirm(`Delete device “${device.token}”? This cannot be undone.`)) return;
    try {
      await deleteDevice(device.token);
      toast(`Device “${device.token}” deleted`);
      navigate('/devices');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={device.name || token}
      banner="devices"
      description={
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <TypeCapsule appearance={appearanceOf(device.deviceType)} />
          <TokenCapsule token={device.token} />
        </div>
      }
      action={
        <Button variant="destructive" size="sm" onClick={remove}>
          <Trash2 size={14} /> Delete
        </Button>
      }
    >
      {/* Connectivity + events sit in their own tabs, so they load only when
          opened and the page stays digestible. */}
      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">Basic</TabsTrigger>
          <TabsTrigger value="assignment">Assignment</TabsTrigger>
          <TabsTrigger value="connectivity">Connectivity</TabsTrigger>
          <TabsTrigger value="events">Events</TabsTrigger>
          <TabsTrigger value="commands">Commands</TabsTrigger>
          <TabsTrigger value="credentials">Credentials</TabsTrigger>
        </TabsList>
        <TabsContent value="basic">
          <SectionPanel>
            <DeviceForm
              device={device}
              onDone={(m) => {
                toast(m);
                reload();
              }}
            />
          </SectionPanel>
        </TabsContent>
        <TabsContent value="assignment">
          <SectionPanel>
            <DeviceAssignmentPanel deviceToken={device.token} />
          </SectionPanel>
        </TabsContent>
        <TabsContent value="connectivity">
          <div className="space-y-4">
            <SectionPanel title="Connectivity">
              <DeviceStatePanel deviceId={device.id} />
            </SectionPanel>
            <SectionPanel title="Latest readings">
              <DeviceReadingsPanel deviceId={device.id} />
            </SectionPanel>
          </div>
        </TabsContent>
        <TabsContent value="events">
          <SectionPanel>
            <DeviceEventsPanel deviceId={device.id} />
          </SectionPanel>
        </TabsContent>
        <TabsContent value="commands">
          <SectionPanel>
            <DeviceCommandsPanel deviceToken={device.token} />
          </SectionPanel>
        </TabsContent>
        <TabsContent value="credentials">
          <SectionPanel>
            <DeviceCredentialsPanel deviceToken={device.token} />
          </SectionPanel>
        </TabsContent>
      </Tabs>
    </PageShell>
  );
}

// DeviceStatePanel loads the device-state projection independently of the rest
// of the page: if the tenant's role lacks state:read the query errors and this
// panel shows an ErrorState rather than breaking the page.
function DeviceStatePanel({ deviceId }: { deviceId: string }) {
  const { data: state, loading, error } = useQuery(
    () => getDeviceState(Number(deviceId)),
    [deviceId],
  );

  if (loading) return <LoadingState description="Loading state…" />;
  if (error) return <ErrorState description={error} />;
  if (!state) return <EmptyState description="No state recorded for this device yet." />;

  return (
    <div className="space-y-4">
      <div>
        {state.active ? (
          <Badge variant="success" className="gap-1">
            <Wifi size={12} /> Online
          </Badge>
        ) : (
          <Badge variant="outline" className="gap-1 text-muted-foreground">
            <WifiOff size={12} /> Offline
          </Badge>
        )}
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
        <dt className="text-muted-foreground">Last connect</dt>
        <dd className="text-foreground">{formatTime(state.lastConnectTime)}</dd>
        <dt className="text-muted-foreground">Last activity</dt>
        <dd className="text-foreground">{formatTime(state.lastActivityTime)}</dd>
        <dt className="text-muted-foreground">Last disconnect</dt>
        <dd className="text-foreground">{formatTime(state.lastDisconnectTime)}</dd>
      </dl>
    </div>
  );
}

// DeviceReadingsPanel shows the current value of each measurement the device has
// reported — the O(1) latest-value projection from device-state. Loads
// independently: if the role lacks state:read the query errors and this panel
// degrades to an ErrorState rather than breaking the page.
function DeviceReadingsPanel({ deviceId }: { deviceId: string }) {
  const { data, loading, error } = useQuery(
    () => getLatestMeasurements(Number(deviceId)),
    [deviceId],
  );

  if (loading) return <LoadingState description="Loading readings…" />;
  if (error) return <ErrorState description={error} />;

  const readings = data ?? [];
  if (readings.length === 0) {
    return <EmptyState description="No measurements received from this device yet." />;
  }

  return (
    <DataTable>
      <DataTableHead>
        <DataTableHeaderCell>Measurement</DataTableHeaderCell>
        <DataTableHeaderCell>Value</DataTableHeaderCell>
        <DataTableHeaderCell>Updated</DataTableHeaderCell>
      </DataTableHead>
      <DataTableBody>
        {readings.map((m) => (
          <DataTableRow key={m.id}>
            <DataTableCell className="font-medium text-foreground">{m.name}</DataTableCell>
            <DataTableCell className="font-mono text-foreground">{m.value ?? '—'}</DataTableCell>
            <DataTableCell className="text-muted-foreground">{formatTime(m.occurredTime)}</DataTableCell>
          </DataTableRow>
        ))}
      </DataTableBody>
    </DataTable>
  );
}

// DeviceEventsPanel loads the most recent events independently of the rest of
// the page: if the tenant's role lacks event:read the query errors and this
// panel shows an ErrorState rather than breaking the page.
function DeviceEventsPanel({ deviceId }: { deviceId: string }) {
  const { data, loading, error } = useQuery(
    () => listEvents({ deviceId, pageNumber: 1, pageSize: 10 }),
    [deviceId],
  );

  if (loading) return <LoadingState description="Loading events…" />;
  if (error) return <ErrorState description={error} />;

  const results = data?.results ?? [];
  if (results.length === 0) {
    return <EmptyState description="No events received from this device yet." />;
  }

  const totalRecords = data?.pagination.totalRecords ?? 0;

  return (
    <div className="space-y-3">
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>Occurred</DataTableHeaderCell>
          <DataTableHeaderCell>Type</DataTableHeaderCell>
          <DataTableHeaderCell>Source</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {results.map((event) => (
            <DataTableRow key={event.id}>
              <DataTableCell className="text-muted-foreground">
                {formatTime(event.occurredTime)}
              </DataTableCell>
              <DataTableCell className="font-mono text-xs text-foreground">
                #{event.eventType}
              </DataTableCell>
              <DataTableCell className="text-muted-foreground">{event.source}</DataTableCell>
            </DataTableRow>
          ))}
        </DataTableBody>
      </DataTable>
      {totalRecords > 10 && (
        <p className="text-xs text-muted-foreground">Showing the 10 most recent events.</p>
      )}
    </div>
  );
}
