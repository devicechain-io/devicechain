// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Configuration tab of a device profile: which shared attribute keys a device of this
// profile is allowed to see as its configuration document. Nothing declared means the
// device sees an empty document.
//
// Like every other tab here this edits the DRAFT; a device resolves the declaration frozen
// into the active published version, so a change takes effect when the profile is
// published (Versions tab). The server stores the declaration as one list, validates it as
// a unit and refuses it whole, so each add / edit / remove sends the full new list and a
// refusal leaves the stored declaration exactly as it was.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Pencil, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Select, SelectItem } from '@/components/ui/select';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { FormDrawer } from '@/components/registry';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import {
  getProfileConfigurationDeclaration,
  setProfileConfigurationDeclaration,
  type ConfigurationKey,
} from '@/lib/api/device-configuration';
import '@/i18n/lazy/deviceProfiles';

// Mirrors the server's attribute-key grammar and ceilings so an obvious mistake is
// caught before the round trip. The server remains the authority and its message is
// shown verbatim when it refuses something this does not catch.
const KEY_GRAMMAR = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;
export const MAX_KEY_LENGTH = 128;
export const MAX_DECLARED_KEYS = 256;
export const MAX_KEY_DESCRIPTION = 256;

export const CONFIG_VALUE_TYPES = ['STRING', 'LONG', 'DOUBLE', 'BOOLEAN', 'JSON'] as const;
const DEVICE_WRITE = 'device:write';
const DASH = '—';

type Row = { key: string; valueType: string; description: string | null };
const toRow = (k: ConfigurationKey): Row => ({
  key: k.key,
  valueType: k.valueType,
  description: k.description ?? null,
});

function KeyForm({
  existing,
  keys,
  onSave,
}: {
  /** The key being edited, or undefined when adding. */
  existing?: Row;
  keys: Row[];
  /** Persists the proposed full list; rejects with the server's message. */
  onSave: (next: Row[]) => Promise<void>;
}) {
  const { t } = useTranslation(['deviceProfiles', 'common']);
  const [key, setKey] = useState(existing?.key ?? '');
  const [valueType, setValueType] = useState(existing?.valueType ?? 'STRING');
  const [description, setDescription] = useState(existing?.description ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    const k = key.trim();
    const desc = description.trim();
    let problem: string | null = null;
    if (!KEY_GRAMMAR.test(k)) problem = t('deviceProfiles:cfgKeyInvalid');
    else if (k.length > MAX_KEY_LENGTH) problem = t('deviceProfiles:cfgKeyTooLong', { max: MAX_KEY_LENGTH });
    else if (keys.some((r) => r.key === k && r.key !== existing?.key))
      problem = t('deviceProfiles:cfgKeyDuplicate', { key: k });
    else if ([...desc].length > MAX_KEY_DESCRIPTION)
      problem = t('deviceProfiles:cfgDescriptionTooLong', { max: MAX_KEY_DESCRIPTION });
    else if (!existing && keys.length >= MAX_DECLARED_KEYS)
      problem = t('deviceProfiles:cfgTooManyKeys', { max: MAX_DECLARED_KEYS });
    if (problem) {
      setFormError(problem);
      return;
    }

    const row: Row = { key: k, valueType, description: desc === '' ? null : desc };
    const next = existing ? keys.map((r) => (r.key === existing.key ? row : r)) : [...keys, row];
    setBusy(true);
    try {
      await onSave(next);
    } catch (err) {
      // The server's refusal is shown as-is; the drawer stays open so nothing typed is lost.
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label={t('deviceProfiles:cfgKeyLabel')}
        htmlFor="cfg-key"
        description={t('deviceProfiles:cfgKeyHint')}
      >
        <Input
          id="cfg-key"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder={t('deviceProfiles:cfgKeyPlaceholder')}
        />
      </FormField>
      <FormField label={t('deviceProfiles:cfgTypeLabel')} htmlFor="cfg-type" description={t('deviceProfiles:cfgTypeHint')}>
        <Select id="cfg-type" value={valueType} onChange={setValueType}>
          {CONFIG_VALUE_TYPES.map((v) => (
            <SelectItem key={v} value={v}>
              {t(`deviceProfiles:cfgType${v}`)}
            </SelectItem>
          ))}
        </Select>
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="cfg-desc">
        <Input id="cfg-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <div className="flex gap-2 pt-1">
        <Button onClick={submit} loading={busy} disabled={busy || !key.trim()}>
          {existing ? t('common:saveChanges') : t('deviceProfiles:cfgAddButton')}
        </Button>
      </div>
    </div>
  );
}

export function ConfigurationDeclarationPanel({ profileToken }: { profileToken: string }) {
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, DEVICE_WRITE);
  const { toast } = useToast();
  const confirm = useConfirm();
  const { t } = useTranslation(['deviceProfiles', 'common']);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => getProfileConfigurationDeclaration(profileToken),
    [profileToken, version],
  );
  const [drawer, setDrawer] = useState<{ open: boolean; existing?: Row }>({ open: false });

  const keys = (data ?? []).map(toRow);
  // Every write sends the WHOLE list built from `keys`, so a write is offered only while
  // `keys` is the current answer. useQuery keeps the previous list both while a re-read is
  // in flight (right after a save) and after one fails; a write built from that list would
  // silently undo the save it follows.
  const fresh = !loading && error == null;

  const save = async (next: Row[]) => {
    await setProfileConfigurationDeclaration(profileToken, next);
    reload();
  };

  const remove = async (row: Row) => {
    if (
      !(await confirm({
        title: t('deviceProfiles:cfgDeleteTitle'),
        description: t('deviceProfiles:cfgDeleteConfirm', { key: row.key }),
        confirmLabel: t('common:delete'),
      }))
    )
      return;
    try {
      await save(keys.filter((r) => r.key !== row.key));
      toast(t('deviceProfiles:cfgDeleted', { key: row.key }));
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const emptyText = canWrite
    ? t('deviceProfiles:cfgEmpty') + ' ' + t('deviceProfiles:cfgEmptyHint')
    : t('deviceProfiles:cfgEmpty');

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <p className="max-w-prose text-sm text-muted-foreground">{t('deviceProfiles:cfgDescription')}</p>
        {canWrite && (
          <Button size="sm" onClick={() => setDrawer({ open: true })} disabled={!fresh} className="shrink-0">
            <Plus size={16} /> {t('deviceProfiles:cfgNew')}
          </Button>
        )}
      </div>

      <FormDrawer
        shot="profile-configuration-key"
        open={drawer.open}
        onOpenChange={(open) => setDrawer((s) => ({ ...s, open }))}
        title={drawer.existing ? t('deviceProfiles:cfgEditTitle') : t('deviceProfiles:cfgNewTitle')}
      >
        {/* Mount the form only while open so each open starts from fresh state. */}
        {drawer.open && (
          <KeyForm
            existing={drawer.existing}
            keys={keys}
            onSave={async (next) => {
              await save(next);
              toast(t(drawer.existing ? 'deviceProfiles:cfgUpdated' : 'deviceProfiles:cfgAdded'));
              setDrawer({ open: false });
            }}
          />
        )}
      </FormDrawer>

      {loading && !data ? (
        <LoadingState description={t('deviceProfiles:cfgLoading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : keys.length === 0 ? (
        <EmptyState description={emptyText} />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('deviceProfiles:cfgColKey')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('common:colDescription')}</DataTableHeaderCell>
            {canWrite && <DataTableHeaderCell className="text-right">{t('common:colActions')}</DataTableHeaderCell>}
          </DataTableHead>
          <DataTableBody>
            {keys.map((r) => (
              <DataTableRow key={r.key}>
                <DataTableCell className="font-mono text-xs">{r.key}</DataTableCell>
                <DataTableCell className="text-muted-foreground">{r.valueType}</DataTableCell>
                <DataTableCell className="text-muted-foreground">{r.description || DASH}</DataTableCell>
                {canWrite && (
                  <DataTableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={!fresh}
                        onClick={() => setDrawer({ open: true, existing: r })}
                      >
                        <Pencil size={14} /> {t('deviceProfiles:defEdit')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={t('deviceProfiles:cfgDeleteTitle')}
                        disabled={!fresh}
                        onClick={() => remove(r)}
                      >
                        <Trash2 size={14} />
                      </Button>
                    </div>
                  </DataTableCell>
                )}
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </div>
  );
}
