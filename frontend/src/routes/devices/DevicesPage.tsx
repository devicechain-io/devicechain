// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useQuery } from '@/lib/hooks/use-query';
import { listDevices } from '@/lib/api/device-management';
import { PageShell } from '@/components/ui/page-shell';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Badge } from '@/components/ui/badge';
import { Pagination } from '@/components/ui/pagination';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

const pageSize = 20;

export default function DevicesPage() {
  const [pageNumber, setPageNumber] = useState(1);
  const { data, loading, error } = useQuery(
    () => listDevices({ pageNumber, pageSize }),
    [pageNumber],
  );

  if (loading) return <LoadingState description="Loading devices…" />;
  if (error) return <ErrorState description={error} />;

  const results = data?.results ?? [];

  return (
    <PageShell title="Devices" description="Devices registered in this tenant (requires device:read)">
      {results.length === 0 ? (
        <EmptyState description="No devices registered yet." />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Token</DataTableHeaderCell>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Type</DataTableHeaderCell>
              <DataTableHeaderCell>Description</DataTableHeaderCell>
              <DataTableHeaderCell>Created</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((device) => (
                <DataTableRow key={device.id}>
                  <DataTableCell>
                    <span className="font-mono text-xs text-foreground">{device.token}</span>
                  </DataTableCell>
                  <DataTableCell className="font-medium text-foreground">
                    {device.name || '—'}
                  </DataTableCell>
                  <DataTableCell>
                    <Badge variant="secondary">
                      {device.deviceType.name || device.deviceType.token}
                    </Badge>
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate text-muted-foreground">
                    {device.description || '—'}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {device.createdAt ? new Date(device.createdAt).toLocaleDateString() : '—'}
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          <Pagination
            pageNumber={pageNumber}
            pageSize={pageSize}
            pagination={data!.pagination}
            onPageChange={setPageNumber}
            className="mt-4"
          />
        </>
      )}
    </PageShell>
  );
}
