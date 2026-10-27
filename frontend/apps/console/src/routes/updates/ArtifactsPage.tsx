// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The artifact catalogue: every update image this tenant has declared, with the one fact an
// operator scans for first, whether anyone has vouched for it.
//
// The service filters by kind, lifecycle state, component and trust, so trust and state are
// real query criteria. It has NO text search, so the search box narrows the page already
// loaded and says so; it must not read as a catalogue-wide search.
//
// Routed page: the AreaGate in App.tsx renders the "not deployed" state on 'absent' only.
// Everything else (including an unanswered discovery) lets the service answer.

import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { useDebouncedValue } from '@/lib/hooks/use-debounced-value';
import { listArtifacts, type ArtifactState, type ArtifactTrustStatus } from '@/lib/api/update-management';
import { useUpdateAccess } from '@/lib/updateAuthority';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { rowLinkProps, useReload } from '@/routes/common';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { FormDrawer } from '@/components/registry';
import { formatBytes } from '@/lib/artifactUpload';
import { artifactStateLabel } from '@/lib/enumLabel';
import { ArtifactStateBadge, DigestChip, TrustBadge } from './badges';
import { NewArtifactForm } from './NewArtifactForm';
import { DownloadsNotice } from './DownloadsNotice';

const pageSize = 50;

// Closed sets, mirroring the schema enums: the labels are localized, the values never are.
const TRUST_OPTIONS = [
  { value: 'SIGNED', labelKey: 'trustSigned' },
  { value: 'UNVERIFIED', labelKey: 'trustUnverified' },
];
const STATE_OPTIONS = ['UPLOADING', 'AVAILABLE', 'RETIRED'];

export default function ArtifactsPage() {
  const { t } = useTranslation('updates');
  const navigate = useNavigate();
  const { canWrite } = useUpdateAccess();
  const [creating, setCreating] = useState(false);
  const [reloadKey, reload] = useReload();
  const [pageNumber, setPageNumber] = useState(1);
  const [search, setSearch] = useState('');
  const [trust, setTrust] = useState('');
  const [state, setState] = useState('');
  const needle = useDebouncedValue(search).trim().toLowerCase();

  // A changed server-side filter re-narrows the set; page 3 of the old one means nothing.
  useEffect(() => setPageNumber(1), [trust, state]);

  const { data, loading, error } = useQuery(
    () =>
      listArtifacts({
        pageNumber,
        pageSize,
        trustStatus: (trust || undefined) as ArtifactTrustStatus | undefined,
        state: (state || undefined) as ArtifactState | undefined,
      }),
    // Keyed by EVERY input: a stale result under a new filter is a wrong list.
    [pageNumber, trust, state, reloadKey],
  );

  const results = useMemo(() => {
    const all = data?.results ?? [];
    if (!needle) return all;
    return all.filter((a) =>
      [a.name, a.version, a.component, a.token, a.digest].some((f) => f.toLowerCase().includes(needle)),
    );
  }, [data, needle]);

  return (
    <PageShell
      title={t('artifactsTitle')}
      description={t('artifactsDescription')}
      banner="devices"
      action={
        <>
          <Input
            className="h-9 w-48"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('searchPlaceholder')}
            aria-label={t('searchLabel')}
          />
          <Combobox
            className="h-9 w-40"
            options={TRUST_OPTIONS.map((o) => ({ value: o.value, label: t(o.labelKey) }))}
            value={trust}
            onChange={setTrust}
            placeholder={t('filterAllTrust')}
            ariaLabel={t('filterTrustLabel')}
          />
          <Combobox
            className="h-9 w-40"
            options={STATE_OPTIONS.map((s) => ({ value: s, label: artifactStateLabel(t, s) }))}
            value={state}
            onChange={setState}
            placeholder={t('filterAllStates')}
            ariaLabel={t('filterStateLabel')}
          />
          {canWrite && (
            <Button onClick={() => setCreating(true)} data-shot="new-button">
              <Plus size={16} /> {t('newArtifact')}
            </Button>
          )}
        </>
      }
    >
      <DownloadsNotice />
      {canWrite && (
        <FormDrawer
          shot="artifact-new"
          open={creating}
          onOpenChange={(open) => {
            setCreating(open);
            // Closing the drawer after a declaration (uploaded or not) must not leave the
            // list without the artifact that now exists.
            if (!open) reload();
          }}
          title={t('newArtifact')}
          description={t('newArtifactDescription')}
        >
          <NewArtifactForm
            onOpen={(token) => {
              setCreating(false);
              navigate(`/updates/artifacts/${encodeURIComponent(token)}`);
            }}
          />
        </FormDrawer>
      )}
      {loading && !data ? (
        <LoadingState description={t('loadingArtifacts')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : (data?.results.length ?? 0) === 0 ? (
        <EmptyState description={t('emptyArtifacts')} />
      ) : (
        <>
          {results.length === 0 ? (
            <EmptyState description={t('noMatchOnPage')} />
          ) : (
            <DataTable>
              <DataTableHead>
                <DataTableHeaderCell>{t('colName')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colVersion')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colComponent')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colTrust')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colState')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colSize')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colDigest')}</DataTableHeaderCell>
              </DataTableHead>
              <DataTableBody>
                {results.map((a) => (
                  <DataTableRow
                    key={a.id}
                    {...rowLinkProps(() => navigate(`/updates/artifacts/${encodeURIComponent(a.token)}`))}
                  >
                    <DataTableCell className="font-medium text-foreground">{a.name}</DataTableCell>
                    <DataTableCell className="whitespace-nowrap">{a.version}</DataTableCell>
                    <DataTableCell className="text-muted-foreground">{a.component}</DataTableCell>
                    <DataTableCell>
                      <TrustBadge status={a.trustStatus} />
                    </DataTableCell>
                    <DataTableCell>
                      <ArtifactStateBadge state={a.state} />
                    </DataTableCell>
                    <DataTableCell className="whitespace-nowrap text-muted-foreground">
                      <span title={t('exactBytes', { bytes: a.sizeBytes })}>{formatBytes(a.sizeBytes)}</span>
                    </DataTableCell>
                    {/* The chip is a button inside a clickable row: stop the click so
                        copying a digest does not also navigate away. */}
                    <DataTableCell>
                      <span
                        className="inline-flex"
                        onClick={(e) => e.stopPropagation()}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        <DigestChip digest={a.digest} />
                      </span>
                    </DataTableCell>
                  </DataTableRow>
                ))}
              </DataTableBody>
            </DataTable>
          )}
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
