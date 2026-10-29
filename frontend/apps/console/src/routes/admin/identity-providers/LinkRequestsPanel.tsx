// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pending link requests: sign-ins that were refused because they matched an existing
// identity that has no link to the external account. A request grants nothing until a
// person approves it here, so each row shows what the approval would join together.
//
// 🔴 APPROVAL LINKS THE EXTERNAL ACCOUNT TO WHOEVER HOLDS THAT EMAIL NOW. Two facts make
// that dangerous and both are surfaced on the row rather than left to the operator's
// memory: the email may have been recycled to a different person (`subjectChanged`), and
// the identity may already hold memberships that no connection manages — in which case
// the server needs to be told what becomes of them, so Approve stays disabled until the
// operator has chosen.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check, X } from 'lucide-react';
import {
  approveSsoLinkRequest,
  listSsoLinkRequests,
  rejectSsoLinkRequest,
  type SsoLinkRequest,
} from '@/lib/api/identity-providers';
import { useQuery } from '@/lib/hooks/use-query';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { useConfirm } from '@/components/ui/confirm-dialog';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';
import { SectionPanel } from '@/components/ui/section-panel';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { useToast } from '@/components/ui/toast';
import { errMessage, useReload } from '@/routes/common';
import '@/i18n/lazy/identityProviders';

type Disposition = 'convert' | 'keep_manual' | '';

const DISPOSITIONS: Disposition[] = ['convert', 'keep_manual'];

export function LinkRequestsPanel() {
  const { t } = useTranslation('identityProviders');
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(listSsoLinkRequests, [version]);
  const [actionError, setActionError] = useState<string | null>(null);

  // useQuery keeps the previous rows while a re-read is in flight, so a row that was just
  // approved or rejected is still on screen, and still clickable, until the read lands.
  // Every write is disabled for that window rather than risking a second click on a row
  // the server no longer has.
  const reading = loading;

  return (
    <SectionPanel title={t('linkRequestsTitle')} description={t('linkRequestsDescription')}>
      {actionError && <ErrorBanner message={actionError} onDismiss={() => setActionError(null)} />}
      {loading && !data ? (
        <LoadingState description={t('loadingLinkRequests')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : !data || data.length === 0 ? (
        <EmptyState description={t('emptyLinkRequests')} />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('colEmail')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colConnection')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colSubject')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colRequested')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colDisposition')}</DataTableHeaderCell>
            <DataTableHeaderCell> </DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {data.map((r) => (
              <LinkRequestRow
                key={r.id}
                request={r}
                disabled={reading}
                onError={setActionError}
                onDone={() => {
                  setActionError(null);
                  reload();
                }}
              />
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </SectionPanel>
  );
}

function LinkRequestRow({
  request: r,
  disabled,
  onError,
  onDone,
}: {
  request: SsoLinkRequest;
  disabled: boolean;
  onError: (message: string) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [disposition, setDisposition] = useState<Disposition>('');
  const [busy, setBusy] = useState(false);

  const hasManual = r.manualMemberships.length > 0;
  // A changed subject means the identity already holds a link on this connection, and the
  // server refuses a second one (one link per identity per connection), so Approve could
  // only ever fail. The hint says what to do instead.
  const approveBlocked =
    !r.identityExists || r.subjectChanged || (hasManual && disposition === '');

  const approve = async () => {
    setBusy(true);
    try {
      await approveSsoLinkRequest(r.id, hasManual && disposition !== '' ? disposition : undefined);
      toast(t('linkApprovedToast', { email: r.email }));
      onDone();
    } catch (err) {
      onError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const reject = async () => {
    if (
      !(await confirm({
        title: t('rejectLinkTitle'),
        description: t('rejectLinkDescription', { email: r.email }),
        confirmLabel: t('rejectButton'),
      }))
    )
      return;
    setBusy(true);
    try {
      await rejectSsoLinkRequest(r.id);
      toast(t('linkRejectedToast', { email: r.email }));
      onDone();
    } catch (err) {
      onError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <DataTableRow>
      <DataTableCell className="font-medium text-foreground">
        <div>{r.email}</div>
        <div className="mt-1 flex flex-wrap gap-1">
          {!r.identityExists && <Badge variant="outline">{t('noIdentityBadge')}</Badge>}
          {r.subjectChanged && <Badge variant="warning">{t('subjectChangedBadge')}</Badge>}
        </div>
        {r.subjectChanged && (
          <p className="mt-1 text-xs font-normal text-muted-foreground">
            {t('subjectChangedHint')}
          </p>
        )}
      </DataTableCell>
      <DataTableCell className="text-muted-foreground">{r.connection}</DataTableCell>
      <DataTableCell className="break-all font-mono text-xs text-muted-foreground">
        {r.subject}
      </DataTableCell>
      <DataTableCell className="text-muted-foreground">{r.createdAt ?? ''}</DataTableCell>
      <DataTableCell>
        {hasManual ? (
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">
              {t('manualMembershipsHint', { tenants: r.manualMemberships.join(', ') })}
            </p>
            <SegmentedControl
              ariaLabel={t('dispositionLabel')}
              tone="joined"
              size="sm"
              value={disposition}
              onValueChange={setDisposition}
              disabled={disabled || busy}
              options={DISPOSITIONS.map((d) => ({ value: d, label: t(`disposition_${d}`) }))}
            />
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">{t('dispositionNotNeeded')}</span>
        )}
      </DataTableCell>
      <DataTableCell className="text-right">
        <div className="flex items-center justify-end gap-1">
          <Button
            size="sm"
            onClick={() => void approve()}
            disabled={disabled || busy || approveBlocked}
            aria-label={t('approveLinkAria', { email: r.email })}
          >
            <Check size={14} /> {t('approveButton')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => void reject()}
            disabled={disabled || busy}
            aria-label={t('rejectLinkAria', { email: r.email })}
          >
            <X size={14} /> {t('rejectButton')}
          </Button>
        </div>
      </DataTableCell>
    </DataTableRow>
  );
}
