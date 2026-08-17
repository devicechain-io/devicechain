// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// One fleet write, read in full: what was fired, at whom, who did not get it, and where
// its commands are now — and the one place it can be CALLED OFF.
//
// 🔴 THE PAGE ANSWERS TWO DIFFERENT TENSES, AND THEY MUST NOT BE CONFLATED. The batch
// record's `resolved` / `accepted` / refusals are STORED FACTS ABOUT THE MOMENT IT FIRED
// (command rows are not immortal, so deriving them live would let them drift below the
// creation-time truth with no refusal explaining the gap). "How many have actually gone
// out?" is a present-tense question, answerable only from the command rows — which is
// what the Commands panel at the bottom queries, separately.

import { useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { CopyToken } from '@/components/ui/copy-token';
import { Badge } from '@/components/ui/badge';
import { Combobox } from '@/components/ui/combobox';
import { Textarea } from '@/components/ui/textarea';
import { HintText } from '@/components/ui/hint-text';
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
import { COMMAND_STATUSES } from '@devicechain/dashboards';
import { formatTime } from '@/lib/utils';
import { commandStatusVariant } from '@/lib/command-status';
import { useQuery } from '@/lib/hooks/use-query';
import { useReload } from '@/routes/common';
import {
  getCommandBatch,
  listBatchCommands,
  type CommandBatch,
} from '@/lib/api/command-delivery';
import { TargetKindBadge } from './TargetKindBadge';
import { Fact } from './Fact';
import { groupRefusals, totalRefused, type RefusalGroup } from './refusals';
import { RefusalGroupList } from './RefusalGroups';
import { CancelBatchButton, CancelBatchReport, useBatchCancel } from './CancelBatchAction';

const commandsPageSize = 25;

export default function CommandBatchDetailPage() {
  const { t } = useTranslation('commandBatches');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = rawToken ?? '';
  // Bumped after a cancellation. Everything on this page goes stale at once — the batch's
  // cancellation stamp and count, the live still-held count behind the confirm dialog, and
  // every command row's status — so one counter drives all three re-reads.
  const [reloadKey, reload] = useReload();
  const { data, loading, error } = useQuery(() => getCommandBatch(token), [token, reloadKey]);

  // First-load gating only: useQuery re-enters `loading` on a refetch while keeping the
  // prior data, so a bare `loading` check would unmount the whole page on every refresh.
  if (loading && !data) {
    return (
      <PageShell title={token} banner="devices">
        <LoadingState description={t('loadingBatch')} />
      </PageShell>
    );
  }
  if (error && !data) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!data) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={t('notFound', { token })} />
      </PageShell>
    );
  }
  return <CommandBatchDetail batch={data} reloadKey={reloadKey} onCancelled={reload} />;
}

function CommandBatchDetail({
  batch,
  reloadKey,
  onCancelled,
}: {
  batch: CommandBatch;
  reloadKey: number;
  onCancelled: () => void;
}) {
  const { t } = useTranslation('commandBatches');
  const groups = groupRefusals(batch.refusalCounts, batch.refusals);
  const refused = totalRefused(batch.refusalCounts);
  // 🔴 THE STATE LIVES HERE, ABOVE BOTH HALVES, and that is what keeps the report on
  // screen: the button is in the header's action slot and the report is in the body, so a
  // component owning both would have to be one or the other. Holding it here also means
  // the refetch this triggers repaints the page underneath a report that stays put.
  const cancelState = useBatchCancel({ batch, reloadKey, onCancelled });

  return (
    <PageShell
      // The batch's name is the COMMAND KEY every member received; its token is the
      // idempotency key the whole fan-out is identified by. Both are identity, so the
      // token rides on the title line as a copy chip.
      title={batch.name}
      titleAdornment={<CopyToken value={batch.token} />}
      banner="devices"
      // The entity's own action, bare — PageShell lays it out. It renders nothing when the
      // operator lacks command:write, or when the batch is stamped cancelled AND a live
      // count says the platform is holding nothing more of it — deliberately not on the
      // stamp alone, which hid the brake on a batch that still had commands running.
      action={<CancelBatchButton state={cancelState} />}
      description={
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <TargetKindBadge targetKind={batch.targetKind} />
          {batch.groupToken && (
            <span className="text-sm text-muted-foreground">
              {batch.groupVersion == null
                ? batch.groupToken
                : t('groupAtVersion', { group: batch.groupToken, version: batch.groupVersion })}
            </span>
          )}
          {/* allowPartial is stated either way rather than only when true: it is the
              field that decided whether a physical actuation was allowed to reach some
              of a fleet but not all of it, so its absence must not read as "unset". */}
          <Badge variant={batch.allowPartial ? 'secondary' : 'outline'}>
            {batch.allowPartial ? t('partialAllowed') : t('allOrNothing')}
          </Badge>
          {batch.cancelledAt && (
            <Badge variant="outline">
              {batch.cancelledCount == null
                ? t('cancelledBadge')
                : t('cancelledBadgeWithCount', { count: batch.cancelledCount })}
            </Badge>
          )}
        </div>
      }
    >
      <div className="space-y-6">
        {/* First, because it is the most recent thing that happened to this batch and the
            operator pressed a button to make it happen. */}
        <CancelBatchReport state={cancelState} />
        <SummaryPanel batch={batch} refused={refused} />
        <RefusalsPanel groups={groups} refused={refused} />
        <BatchCommandsPanel batchToken={batch.token} reloadKey={reloadKey} />
      </div>
    </PageShell>
  );
}

// ── Summary ────────────────────────────────────────────────────────────────

function SummaryPanel({ batch, refused }: { batch: CommandBatch; refused: number }) {
  const { t } = useTranslation('commandBatches');
  // 🔴 The identity the service guarantees: resolved = accepted + Σ refusalCounts. It is
  // stated on screen rather than left for the reader to derive, because the three numbers
  // are otherwise just three numbers — and if they ever stop adding up, the operator is
  // the one who needs to know that the record of a fleet actuation is inconsistent.
  const adds = batch.accepted + refused === batch.resolved;
  return (
    <SectionPanel title={t('summaryTitle')}>
      <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Fact label={t('colFired')}>{formatTime(batch.createdAt)}</Fact>
        <Fact label={t('factResolved')}>{batch.resolved}</Fact>
        <Fact label={t('factAccepted')}>{batch.accepted}</Fact>
        <Fact label={t('factRefused')}>{refused}</Fact>
      </dl>
      <HintText size="md" className="mt-3">
        {t('arithmetic', {
          resolved: batch.resolved,
          accepted: batch.accepted,
          refused,
        })}
      </HintText>
      {!adds && (
        <p className="mt-1 text-sm text-destructive">{t('arithmeticMismatch')}</p>
      )}
      <HintText size="md" className="mt-1">{t('storedFactsHint')}</HintText>
      {batch.cancelledAt && (
        <p className="mt-3 text-sm text-muted-foreground">
          {batch.cancelledCount == null
            ? t('cancelledAtOnly', { when: formatTime(batch.cancelledAt) })
            : t('cancelledAtWithCount', {
                when: formatTime(batch.cancelledAt),
                count: batch.cancelledCount,
              })}
        </p>
      )}
      <div className="mt-4">
        <p className="mb-1 text-xs uppercase tracking-wider text-muted-foreground">
          {t('payloadLabel')}
        </p>
        {batch.payload ? (
          <Textarea
            readOnly
            rows={5}
            value={batch.payload}
            aria-label={t('payloadLabel')}
            className="bg-muted/30"
          />
        ) : (
          <p className="text-sm text-muted-foreground">{t('noPayload')}</p>
        )}
      </div>
    </SectionPanel>
  );
}

// ── Refusals ───────────────────────────────────────────────────────────────

function RefusalsPanel({ groups, refused }: { groups: RefusalGroup[]; refused: number }) {
  const { t } = useTranslation('commandBatches');
  return (
    <SectionPanel
      title={t('refusalsTitle')}
      description={groups.length === 0 ? undefined : t('refusalsDescription', { count: refused })}
    >
      {groups.length === 0 ? (
        <EmptyState description={t('noRefusals')} />
      ) : (
        <RefusalGroupList groups={groups} />
      )}
    </SectionPanel>
  );
}

// ── Commands ───────────────────────────────────────────────────────────────

function BatchCommandsPanel({ batchToken, reloadKey }: { batchToken: string; reloadKey: number }) {
  const { t } = useTranslation('commandBatches');
  const [pageNumber, setPageNumber] = useState(1);
  const [status, setStatus] = useState('');
  useEffect(() => setPageNumber(1), [status]);

  // reloadKey is in the deps because a cancellation changes the STATUS of these rows —
  // this panel is the present tense, and after a cancel the present has moved.
  const { data, loading, error } = useQuery(
    () =>
      listBatchCommands({
        batchToken,
        pageNumber,
        pageSize: commandsPageSize,
        status: status || undefined,
      }),
    [batchToken, pageNumber, status, reloadKey],
  );

  const commands = data?.results ?? [];

  return (
    <SectionPanel
      title={t('commandsTitle')}
      description={t('commandsDescription')}
      action={
        <Combobox
          className="h-9 w-44"
          // The lifecycle vocabulary as the frontend holds it — one shared list, so this
          // filter cannot fall behind the statuses the badges already know about. The
          // values are the wire strings and are never localized.
          options={COMMAND_STATUSES.map((s) => ({ value: s, label: s }))}
          value={status}
          onChange={setStatus}
          placeholder={t('filterAllStatuses')}
          ariaLabel={t('filterStatusLabel')}
        />
      }
    >
      {loading && !data ? (
        <LoadingState description={t('loadingCommands')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : commands.length === 0 ? (
        <EmptyState description={t('noCommands')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colDevice')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colQueued')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colResult')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {/* 🔴 NOT RE-SORTED. These arrive in ADMITTED order — the order the caller
                  named the devices in, which is the order a partially-admitted batch
                  admitted them in — so page 1 is the head of the batch. Sorting here
                  would destroy the one ordering that carries meaning. */}
              {commands.map((c) => (
                <DataTableRow key={c.id}>
                  <DataTableCell className="font-mono text-foreground">{c.deviceToken}</DataTableCell>
                  <DataTableCell>
                    <Badge variant={commandStatusVariant(c.status)}>{c.status}</Badge>
                  </DataTableCell>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {formatTime(c.queuedTime)}
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate text-muted-foreground">
                    {c.error || c.responsePayload || '—'}
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          <Pagination
            pageNumber={pageNumber}
            pageSize={commandsPageSize}
            pagination={data!.pagination}
            onPageChange={setPageNumber}
            className="mt-4"
          />
        </>
      )}
    </SectionPanel>
  );
}
