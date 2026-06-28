// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { getDeviceType, deleteDeviceType } from '@/lib/api/device-management';
import { BackLink, errMessage, useReload } from '@/routes/common';
import { DeviceTypeForm } from '@/routes/device-types/DeviceTypeForm';

export default function DeviceTypeDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();

  const [version, reload] = useReload();
  const { data: deviceType, loading, error } = useQuery(() => getDeviceType(token), [version]);

  const back = <BackLink to="/device-types">Device types</BackLink>;

  if (loading) {
    return (
      <PageShell title={token} action={back}>
        <LoadingState description="Loading device type…" />
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
  if (!deviceType) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={`Device type “${token}” not found.`} />
      </PageShell>
    );
  }

  const remove = async () => {
    if (!window.confirm(`Delete device type “${deviceType.token}”? This cannot be undone.`)) return;
    try {
      await deleteDeviceType(deviceType.token);
      toast(`Device type “${deviceType.token}” deleted`);
      navigate('/device-types');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={token}
      description={deviceType.name ?? '—'}
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
        <DeviceTypeForm
          deviceType={deviceType}
          onDone={(m) => {
            toast(m);
            reload();
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}
