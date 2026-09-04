// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Copy, Replace } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { IconButton } from '@/components/ui/icon-button';
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
import { formatTime } from '@/lib/utils';
import { useNoAutofill } from '@/lib/hooks/use-no-autofill';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { listDeviceReplacements, replaceDevice } from '@/lib/api/replacements';

// DeviceReplacementPanel is the field-tech door for swapping the physical unit
// behind a device: it retires every credential the failed unit could authenticate
// with, mints one for the incoming unit, and shows the append-only history of
// past swaps.
//
// 🔴 THE MINTED CREDENTIAL IS SHOWN EXACTLY ONCE, and that is a property of the
// system rather than a choice this panel makes. The replacement record stores the
// new credential's ENTITY TOKEN, not its id, so nothing here can re-fetch the
// material later — reading it again means going to the separately-gated credential
// queries. The banner therefore stays up until the operator dismisses it, and the
// copy button is the only way the value leaves this screen.
//
// The mutation needs device:write (the result carries a bearer); the history reads
// at device:read. DeviceDetailPage mounts the whole tab only for a device:write
// holder, so the form is never shown to someone it would 403 for.
export function DeviceReplacementPanel({ deviceToken }: { deviceToken: string }) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [reason, setReason] = useState('');
  const [unitIdentifier, setUnitIdentifier] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [minted, setMinted] = useState<{ credentialType: string; credentialId: string } | null>(null);
  const [version, reload] = useReload();
  // The minted credential is device material, not the operator's own login, so
  // suppress password-manager autofill on the annotation fields alongside it.
  const { noAutofill, rearm } = useNoAutofill();

  const { data, loading, error } = useQuery(
    () => listDeviceReplacements(deviceToken, { pageNumber: 1, pageSize: 50 }),
    [deviceToken, version],
  );

  const copy = (text: string) => {
    void navigator.clipboard?.writeText(text);
    toast(t('copiedToClipboard'));
  };

  const submit = async () => {
    // Confirmed rather than done on the click: this retires every credential the
    // outgoing unit holds, so a device that is actually healthy stops talking the
    // moment it lands. It is not undoable from this screen.
    if (
      !(await confirm({
        title: t('replaceDeviceTitle'),
        description: t('replaceDeviceConfirm'),
        confirmLabel: t('replaceDeviceAction'),
      }))
    ) {
      return;
    }
    setSubmitting(true);
    try {
      const result = await replaceDevice({
        deviceToken,
        reason: reason.trim() || undefined,
        unitIdentifier: unitIdentifier.trim() || undefined,
      });
      setMinted({
        credentialType: result.newCredential.credentialType,
        credentialId: result.newCredential.credentialId,
      });
      toast(t('replaceDeviceDone'));
      setReason('');
      setUnitIdentifier('');
      rearm();
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const replacements = data?.results ?? [];

  return (
    <div className="space-y-6">
      {/* The one-time credential banner. It is above the form so it cannot be
          scrolled past, and it survives a reload of the history below it. */}
      {minted && (
        <div className="space-y-2 rounded-lg border border-warning bg-warning/10 p-4">
          <div className="text-sm font-medium text-foreground">{t('newUnitCredentialTitle')}</div>
          <p className="text-xs text-muted-foreground">{t('newUnitCredentialHint')}</p>
          <div className="flex items-center gap-1.5">
            <Badge variant="secondary">{minted.credentialType}</Badge>
            <span className="font-mono text-xs break-all text-foreground">{minted.credentialId}</span>
            <IconButton
              label={t('copyIdAriaLabel')}
              variant="quiet"
              size="xs"
              onClick={() => copy(minted.credentialId)}
            >
              <Copy size={13} />
            </IconButton>
          </div>
          <Button variant="outline" size="sm" onClick={() => setMinted(null)}>
            {t('dismissCredential')}
          </Button>
        </div>
      )}

      <div className="space-y-4 rounded-lg border border-border bg-muted/40 p-4">
        <p className="text-xs text-muted-foreground">{t('replaceDeviceHint')}</p>
        <FormField label={t('replacementReasonLabel')} description={t('replacementReasonHint')}>
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t('replacementReasonPlaceholder')}
            {...noAutofill}
          />
        </FormField>
        <FormField label={t('newUnitIdentifierLabel')} description={t('newUnitIdentifierHint')}>
          <Input
            value={unitIdentifier}
            onChange={(e) => setUnitIdentifier(e.target.value)}
            placeholder={t('newUnitIdentifierPlaceholder')}
            {...noAutofill}
          />
        </FormField>
        <Button onClick={submit} loading={submitting} disabled={submitting}>
          <Replace size={14} /> {t('replaceDeviceAction')}
        </Button>
      </div>

      {loading ? (
        <LoadingState description={t('loadingReplacements')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : replacements.length === 0 ? (
        <EmptyState description={t('noReplacements')} />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('replacedOnColumn')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('replacedByColumn')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('replacementReasonColumn')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('newUnitColumn')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('retiredCredentialsColumn')}</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {replacements.map((r) => (
              <DataTableRow key={r.id}>
                <DataTableCell>{formatTime(r.occurredTime)}</DataTableCell>
                <DataTableCell className="text-muted-foreground">{r.actor || '—'}</DataTableCell>
                <DataTableCell className="text-muted-foreground">{r.reason || '—'}</DataTableCell>
                <DataTableCell className="font-mono text-xs">{r.unitIdentifier || '—'}</DataTableCell>
                {/* A count, not the tokens: the number is what an operator reads,
                    and a list of opaque uuids in a table cell is noise. */}
                <DataTableCell className="text-muted-foreground">
                  {r.retiredCredentialTokens.length}
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </div>
  );
}
