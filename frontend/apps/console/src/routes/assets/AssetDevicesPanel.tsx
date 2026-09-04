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
import { listAssetDevices } from '@/lib/api/relationships';

// How many attached devices one page shows. The total is displayed alongside, so a
// larger fleet reads as "10 of 240" rather than as a complete list that happens to
// be short.
const PAGE = { pageNumber: 1, pageSize: 100 };

// AssetDevicesPanel answers "what is measuring this asset?" — the reverse of the
// device's own assignment panel, over exactly the same tracked edges read from the
// other end.
//
// It closes the gap that made the asset surface headless: assignment could only be
// seen from the DEVICE side, so an operator standing on an asset had to enumerate
// devices to find the ones pointing at it. Nothing new is stored for this.
//
// 🔴 READ-ONLY, DELIBERATELY, AND THAT IS A SCOPE STATEMENT RATHER THAN AN
// OVERSIGHT. Assigning and unassigning stay on the device's own panel, which is
// where the operator already chooses among customer/area/asset targets; a second
// authoring surface for the same edge would be two places to keep in agreement
// about one fact.
export function AssetDevicesPanel({ assetToken }: { assetToken: string }) {
  const { t } = useTranslation('entities');
  const { data, loading, error } = useQuery(() => listAssetDevices(assetToken, PAGE), [assetToken]);

  const edges = data?.results ?? [];
  const total = data?.pagination.totalRecords ?? 0;

  if (loading) {
    return <LoadingState description={t('assetDevicesLoading')} />;
  }
  if (error) {
    return <ErrorState description={error} />;
  }
  if (edges.length === 0) {
    return <EmptyState description={t('assetNoDevices')} />;
  }

  return (
    <div className="space-y-2">
      <p className="text-xs text-muted-foreground">
        {t('assetDevicesCount', { shown: edges.length, total })}
      </p>
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {edges.map((e) => (
            <DataTableRow key={e.id}>
              <DataTableCell>
                <Link to={`/devices/${e.source.token}`} className="text-primary hover:underline">
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
