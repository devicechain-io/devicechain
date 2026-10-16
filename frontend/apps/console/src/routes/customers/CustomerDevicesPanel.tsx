// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
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
import { useQuery } from '@/lib/hooks/use-query';
import { listCustomerDevices } from '@/lib/api/relationships';

// One page of assigned devices; the total is shown beside it so a short page is never
// read as the whole list.
const PAGE = { pageNumber: 1, pageSize: 100 };

// CustomerDevicesPanel lists the devices assigned to this customer: the devices whose
// data the customer's users see. Read-only, like the asset's devices tab — assigning and
// unassigning stay on the device's own Assignment tab, the one place that edge is authored.
export function CustomerDevicesPanel({ customerToken }: { customerToken: string }) {
  const { t } = useTranslation('members');
  const { data, loading, error } = useQuery(() => listCustomerDevices(customerToken, PAGE), [customerToken]);
  const edges = data?.results ?? [];
  const total = data?.pagination.totalRecords ?? 0;

  if (loading) return <LoadingState description={t('customerDevicesLoading')} />;
  if (error) return <ErrorState description={error} />;
  if (edges.length === 0) return <EmptyState description={t('customerDevicesEmpty')} />;

  return (
    <div className="space-y-2">
      <p className="text-xs text-muted-foreground">{t('customerDevicesCount', { shown: edges.length, total })}</p>
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {edges.map((e) => (
            <DataTableRow key={e.id}>
              <DataTableCell>
                <Link to={`/devices/${encodeURIComponent(e.source.token)}`} className="text-primary hover:underline">
                  {e.source.token}
                </Link>
              </DataTableCell>
            </DataTableRow>
          ))}
        </DataTableBody>
      </DataTable>
    </div>
  );
}
