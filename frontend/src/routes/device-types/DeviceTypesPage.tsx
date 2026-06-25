// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useQuery } from '@/lib/hooks/use-query';
import { listDeviceTypes } from '@/lib/api/device-management';
import { PageShell } from '@/components/ui/page-shell';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
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

export default function DeviceTypesPage() {
  const [pageNumber, setPageNumber] = useState(1);
  const { data, loading, error } = useQuery(
    () => listDeviceTypes({ pageNumber, pageSize }),
    [pageNumber],
  );

  if (loading) return <LoadingState description="Loading device types…" />;
  if (error) return <ErrorState description={error} />;

  const results = data?.results ?? [];

  return (
    <PageShell
      title="Device Types"
      description="Templates that classify devices (requires devicetype:read)"
    >
      {results.length === 0 ? (
        <EmptyState description="No device types defined yet." />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Token</DataTableHeaderCell>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Description</DataTableHeaderCell>
              <DataTableHeaderCell>Created</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((deviceType) => (
                <DataTableRow key={deviceType.id}>
                  <DataTableCell>
                    <div className="flex items-center gap-2">
                      <span
                        className={
                          deviceType.backgroundColor
                            ? 'size-4 shrink-0 rounded'
                            : 'size-4 shrink-0 rounded bg-muted'
                        }
                        style={
                          deviceType.backgroundColor
                            ? { backgroundColor: deviceType.backgroundColor }
                            : undefined
                        }
                        aria-hidden
                      />
                      <span className="font-mono text-xs text-foreground">{deviceType.token}</span>
                    </div>
                  </DataTableCell>
                  <DataTableCell className="font-medium text-foreground">
                    {deviceType.name || '—'}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {deviceType.description || '—'}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {deviceType.createdAt
                      ? new Date(deviceType.createdAt).toLocaleDateString()
                      : '—'}
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
