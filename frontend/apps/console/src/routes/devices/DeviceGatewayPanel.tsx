// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Plus, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
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
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { assignDeviceGateway, clearDeviceGateway, getDeviceGateway } from '@/lib/api/device-management';

// DeviceGatewayPanel manages which gateway a device is behind, and lists the devices behind it
// when it is itself a gateway.
//
// A gateway is an ordinary device that fronts other devices: one credential at a site publishes
// for each device behind it, on the gateway's own topics. The platform accepts a gateway's
// publish for a device only when this assignment names that gateway, so assigning is the same
// act of trust as issuing the device a credential; that is why the actions are offered only to a
// member who may manage credentials (`canWrite`), and why the server refuses everyone else.
//
// Gateways are one level deep: the server refuses a device as its own gateway, a gateway that is
// itself behind a gateway, and a device that is itself a gateway, and those refusals are shown
// as they arrive rather than guessed at here.
export function DeviceGatewayPanel({ deviceToken, canWrite }: { deviceToken: string; canWrite: boolean }) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [gatewayToken, setGatewayToken] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [version, reload] = useReload();

  const { data, loading, error } = useQuery(() => getDeviceGateway(deviceToken), [deviceToken, version]);

  const assign = async () => {
    const token = gatewayToken.trim();
    if (!token) {
      toast(t('gatewayChooseFirst'), 'error');
      return;
    }
    setSubmitting(true);
    try {
      await assignDeviceGateway(deviceToken, token);
      toast(t('gatewayAssigned', { token }));
      setGatewayToken('');
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const clear = async () => {
    if (
      !(await confirm({
        title: t('gatewayClearTitle'),
        description: t('gatewayClearConfirm', { token: data?.gateway?.token ?? '' }),
        confirmLabel: t('gatewayClear'),
        destructive: true,
      }))
    ) {
      return;
    }
    try {
      await clearDeviceGateway(deviceToken);
      toast(t('gatewayCleared'));
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  if (loading) return <LoadingState description={t('gatewayLoading')} />;
  if (error) return <ErrorState description={error} />;
  if (!data) return <EmptyState description={t('deviceNotFound', { token: deviceToken })} />;

  const gateway = data.gateway;
  const children = data.children.results;
  const total = data.children.pagination.totalRecords ?? children.length;

  return (
    <div className="space-y-6">
      <p className="text-sm text-muted-foreground">{t('gatewayHint')}</p>

      <div className="space-y-3 rounded-lg border border-border bg-muted/40 p-4">
        {gateway ? (
          <div className="flex flex-wrap items-center gap-3">
            <span className="text-sm text-muted-foreground">{t('gatewayBehind')}</span>
            <Link to={`/devices/${encodeURIComponent(gateway.token)}`} className="font-mono text-xs underline">
              {gateway.token}
            </Link>
            {gateway.name && <span className="text-sm text-foreground">{gateway.name}</span>}
            {!gateway.enabled && <Badge variant="warning">{t('deviceDisabledBadge')}</Badge>}
            {canWrite && (
              <Button variant="outline" size="sm" onClick={clear}>
                <Trash2 size={13} /> {t('gatewayClear')}
              </Button>
            )}
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t('gatewayNone')}</p>
        )}
        {canWrite && (
          <div className="flex flex-wrap items-end gap-3">
            <div className="w-72">
              <FormField label={gateway ? t('gatewayMoveLabel') : t('gatewayLabel')}>
                <Input
                  value={gatewayToken}
                  onChange={(e) => setGatewayToken(e.target.value)}
                  placeholder={t('gatewayPlaceholder')}
                  autoComplete="off"
                />
              </FormField>
            </div>
            <Button onClick={assign} loading={submitting} disabled={submitting}>
              <Plus size={14} /> {gateway ? t('gatewayMove') : t('gatewayAssign')}
            </Button>
          </div>
        )}
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium text-foreground">{t('gatewayChildrenTitle')}</h3>
        {children.length === 0 ? (
          <EmptyState description={t('gatewayNoChildren')} />
        ) : (
          <>
            <DataTable>
              <DataTableHead>
                <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
                <DataTableHeaderCell>&nbsp;</DataTableHeaderCell>
              </DataTableHead>
              <DataTableBody>
                {children.map((child) => (
                  <DataTableRow key={child.token}>
                    <DataTableCell className="font-mono text-xs">
                      <Link to={`/devices/${encodeURIComponent(child.token)}`} className="underline">
                        {child.token}
                      </Link>
                    </DataTableCell>
                    <DataTableCell>{child.name ?? '—'}</DataTableCell>
                    <DataTableCell>
                      {!child.enabled && <Badge variant="warning">{t('deviceDisabledBadge')}</Badge>}
                    </DataTableCell>
                  </DataTableRow>
                ))}
              </DataTableBody>
            </DataTable>
            {total > children.length && (
              <p className="text-xs text-muted-foreground">
                {t('gatewayChildrenShown', { shown: children.length, total })}
              </p>
            )}
          </>
        )}
      </div>
    </div>
  );
}
