// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { rowLinkProps, useReload } from '@/routes/common';
import { FormDrawer } from '@/components/registry/FormDrawer';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import type { RegistryResource } from '@/components/registry/types';

const pageSize = 20;

// Generic paginated list for a registry resource: a "New" action that opens the
// create drawer, clickable rows that route to the detail page, and the resource's
// own columns.
export function ResourceListPage<T>({ resource }: { resource: RegistryResource<T> }) {
  const { t } = useTranslation(['entities', 'common']);
  // Resolve one of this family's noun-bearing strings by its fixed suffix.
  const e = (suffix: string) => t(`entities:${resource.i18nKey}${suffix}`);
  const navigate = useNavigate();
  const { toast } = useToast();
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => resource.list({ pageNumber, pageSize }),
    [pageNumber, version, resource.basePath],
  );

  const results = data?.results ?? [];

  return (
    <PageShell
      title={e('TitlePlural')}
      description={e('ListDescription')}
      banner={resource.banner}
      action={
        <Button onClick={() => setCreating(true)}>
          <Plus size={16} /> {e('New')}
        </Button>
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title={e('New')}>
        {resource.renderForm(undefined, (m) => {
          toast(m);
          setCreating(false);
          reload();
        })}
      </FormDrawer>
      {loading ? (
        <LoadingState description={t('common:loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={e('ListEmpty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              {resource.columns.map((c) => (
                <DataTableHeaderCell key={c.header}>{t(c.header)}</DataTableHeaderCell>
              ))}
            </DataTableHead>
            <DataTableBody>
              {results.map((item) => (
                <DataTableRow
                  key={resource.idOf(item)}
                  {...rowLinkProps(() =>
                    navigate(`${resource.basePath}/${encodeURIComponent(resource.tokenOf(item))}`),
                  )}
                >
                  {resource.columns.map((c) => (
                    <DataTableCell key={c.header} className={c.className}>
                      {c.cell(item)}
                    </DataTableCell>
                  ))}
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
