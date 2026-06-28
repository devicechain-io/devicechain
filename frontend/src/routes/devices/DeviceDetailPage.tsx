// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { getDevice, deleteDevice } from '@/lib/api/device-management';
import { BackLink, errMessage, useReload } from '@/routes/common';
import { DeviceForm } from '@/routes/devices/DeviceForm';

export default function DeviceDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();

  const [version, reload] = useReload();
  const { data: device, loading, error } = useQuery(() => getDevice(token), [version]);

  const back = <BackLink to="/devices">Devices</BackLink>;

  if (loading) {
    return (
      <PageShell title={token} action={back}>
        <LoadingState description="Loading device…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!device) {
    return (
      <PageShell title={token} action={back}>
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
      title={token}
      description={
        <div className="mt-1 flex items-center gap-2">
          <Badge variant="secondary">{device.deviceType.name ?? device.deviceType.token}</Badge>
          {device.name && <span className="text-sm text-muted-foreground">{device.name}</span>}
        </div>
      }
      action={
        <div className="flex items-center gap-2">
          {back}
          <Button variant="destructive" size="sm" onClick={remove}>
            <Trash2 size={14} /> Delete
          </Button>
        </div>
      }
    >
      <SectionPanel>
        <DeviceForm
          device={device}
          onDone={(m) => {
            toast(m);
            reload();
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}
