// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The notification-channel list: the delivery endpoints (email, webhook, Slack, Microsoft
// Teams, PagerDuty) a notification policy routes alarms through. Create and edit share one
// drawer form; the credential is write-only, so an edit shows only whether one is stored and
// replaces it only when a new one is typed.

import { useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useQuery } from '@/lib/hooks/use-query';
import {
  createNotificationChannel,
  deleteNotificationChannel,
  listNotificationChannelRecords,
  listNotificationChannelTypes,
  updateNotificationChannel,
  type NotificationChannelRecord,
} from '@/lib/api/notifications';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { HintText } from '@/components/ui/hint-text';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { Select, SelectItem } from '@/components/ui/select';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useAuth } from '@/auth/AuthProvider';
import { errMessage, rowLinkProps, useReload } from '@/routes/common';
import { FormDrawer } from '@/components/registry';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import {
  REGIONS,
  createRequest,
  draftOf,
  draftProblem,
  editRequest,
  emptyDraft,
  isNative,
  type ChannelDraft,
} from './channelRequest';

const pageSize = 20;
// Technical identifiers, named so the literal-string lint does not read them as UI text.
const NEW = 'new' as const;
const SOURCE_PLACEHOLDER = 'devicechain';
const LINK_PLACEHOLDER = 'https://console.example.com';

export default function NotificationChannelsPage() {
  const { t } = useTranslation(['entities', 'common']);
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'notification:write');
  const [pageNumber, setPageNumber] = useState(1);
  const [editing, setEditing] = useState<NotificationChannelRecord | typeof NEW | null>(null);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listNotificationChannelRecords({ pageNumber, pageSize }),
    [pageNumber, version],
  );
  const types = useQuery(() => listNotificationChannelTypes(), []);
  const labelOf = (id: string) => types.data?.find((x) => x.id === id)?.label ?? id;

  const results = data?.results ?? [];

  const remove = async (token: string) => {
    if (
      !(await confirm({
        title: t('entities:notificationChannelDeleteTitle'),
        description: t('entities:notificationChannelRemoveConfirm', { token }),
        confirmLabel: t('common:delete'),
      }))
    )
      return;
    try {
      await deleteNotificationChannel(token);
      toast(t('entities:notificationChannelDeletedToast', { token }));
      if (results.length === 1 && pageNumber > 1) setPageNumber(pageNumber - 1);
      else reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={t('entities:notificationChannelTitlePlural')}
      description={t('entities:notificationChannelListDescription')}
      banner="dashboard"
      action={
        canWrite ? (
          <Button onClick={() => setEditing(NEW)} data-shot="new-button">
            <Plus size={16} /> {t('entities:notificationChannelNew')}
          </Button>
        ) : undefined
      }
    >
      <FormDrawer
        shot="notification-channel"
        open={editing !== null}
        onOpenChange={(open) => !open && setEditing(null)}
        title={
          editing === NEW || editing === null
            ? t('entities:notificationChannelNew')
            : t('entities:notificationChannelEdit', { token: editing.token })
        }
      >
        {editing !== null && (
          <ChannelForm
            key={editing === NEW ? NEW : editing.token}
            entity={editing === NEW ? undefined : editing}
            canWrite={canWrite}
            types={types.data ?? []}
            onDone={(message) => {
              toast(message);
              setEditing(null);
              reload();
            }}
          />
        )}
      </FormDrawer>
      {loading && !data ? (
        <LoadingState description={t('common:loading')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('entities:notificationChannelListEmpty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('entities:notificationChannelColEnabled')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('entities:notificationChannelColSecret')}</DataTableHeaderCell>
              <DataTableHeaderCell> </DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((c) => (
                <DataTableRow key={c.token} {...rowLinkProps(() => setEditing(c))}>
                  <DataTableCell className="font-medium text-foreground">{c.name || c.token}</DataTableCell>
                  <DataTableCell className="font-mono text-xs text-muted-foreground">{c.token}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{labelOf(c.channelType)}</DataTableCell>
                  <DataTableCell>{c.enabled ? t('common:enabled') : t('common:disabled')}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {c.hasSecret ? t('entities:notificationChannelSecretStored') : '—'}
                  </DataTableCell>
                  <DataTableCell className="text-right">
                    {canWrite && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(c.token);
                        }}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        <Trash2 size={14} /> {t('common:delete')}
                      </Button>
                    )}
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

function ChannelForm({
  entity,
  canWrite,
  types,
  onDone,
}: {
  entity?: NotificationChannelRecord;
  canWrite: boolean;
  types: { id: string; label: string; available: boolean }[];
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const editing = entity !== undefined;
  const [token, setToken] = useState(entity?.token ?? '');
  const [draft, setDraft] = useState<ChannelDraft>(() => (entity ? draftOf(entity) : emptyDraft('slack')));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<ChannelDraft>) => setDraft((d) => ({ ...d, ...patch }));

  const native = isNative(draft.channelType);
  const problem = draftProblem(draft, entity?.hasSecret ?? false);
  const request = entity ? editRequest(entity, draft) : null;
  const dirty = request !== null && Object.keys(request).length > 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (entity && request) {
        await updateNotificationChannel(entity.token, request);
        onDone(t('entities:notificationChannelUpdatedToast', { token: entity.token }));
      } else {
        const trimmed = token.trim();
        await createNotificationChannel(createRequest(trimmed, draft));
        onDone(t('entities:notificationChannelCreatedToast', { token: trimmed }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const secretLabel =
    draft.channelType === 'pagerduty'
      ? t('entities:notificationChannelSecretRoutingKey')
      : native
        ? t('entities:notificationChannelSecretWebhookUrl')
        : t('entities:notificationChannelSecretGeneric');
  const secretHelp =
    draft.channelType === 'slack'
      ? t('entities:notificationChannelSecretSlackHelp')
      : draft.channelType === 'teams'
        ? t('entities:notificationChannelSecretTeamsHelp')
        : draft.channelType === 'pagerduty'
          ? t('entities:notificationChannelSecretPagerDutyHelp')
          : t('entities:notificationChannelSecretGenericHelp');

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {!canWrite && <HintText size="md">{t('entities:notificationChannelReadOnly')}</HintText>}
      <fieldset disabled={!canWrite} className="contents">
        <FormField label={t('common:colType')} htmlFor="nc-type">
          <Select
            id="nc-type"
            value={draft.channelType}
            disabled={editing}
            onChange={(v) => set({ channelType: v })}
          >
            {types
              .filter((x) => x.available)
              .map((x) => (
                <SelectItem key={x.id} value={x.id}>
                  {x.label}
                </SelectItem>
              ))}
          </Select>
        </FormField>
        <FormField
          label={t('common:colToken')}
          htmlFor="nc-token"
          description={editing ? t('entities:notificationChannelTokenFixed') : undefined}
        >
          {editing ? (
            <Input id="nc-token" value={token} disabled />
          ) : (
            <TokenField
              id="nc-token"
              entityType="notification-channel"
              value={token}
              onChange={setToken}
              seed={draft.name}
              placeholder={t('entities:notificationChannelTokenPlaceholder')}
            />
          )}
        </FormField>
        <FormField label={t('common:colName')} htmlFor="nc-name">
          <Input id="nc-name" value={draft.name} onChange={(e) => set({ name: e.target.value })} />
        </FormField>
        <FormField label={t('common:colDescription')} htmlFor="nc-description">
          <Textarea
            id="nc-description"
            value={draft.description}
            onChange={(e) => set({ description: e.target.value })}
          />
        </FormField>
        <label className="flex items-center gap-2 text-sm">
          <Checkbox checked={draft.enabled} onCheckedChange={(c) => set({ enabled: c === true })} />
          <span>{t('common:enabled')}</span>
          <span className="text-muted-foreground">{t('entities:notificationChannelEnabledHint')}</span>
        </label>

        <FormField
          label={secretLabel}
          htmlFor="nc-secret"
          description={
            entity?.hasSecret ? t('entities:notificationChannelSecretKeepHint') : secretHelp
          }
        >
          <Input
            id="nc-secret"
            type="password"
            autoComplete="off"
            value={draft.secret}
            placeholder={entity?.hasSecret ? t('entities:notificationChannelSecretStoredPlaceholder') : ''}
            onChange={(e) => set({ secret: e.target.value })}
          />
        </FormField>

        {draft.channelType === 'pagerduty' && (
          <>
            <FormField
              label={t('entities:notificationChannelRegion')}
              htmlFor="nc-region"
              description={t('entities:notificationChannelRegionHelp')}
            >
              <Select id="nc-region" value={draft.region} onChange={(v) => set({ region: v })}>
                {REGIONS.map((r) => (
                  <SelectItem key={r} value={r}>
                    {r.toUpperCase()}
                  </SelectItem>
                ))}
              </Select>
            </FormField>
            <FormField
              label={t('entities:notificationChannelSource')}
              htmlFor="nc-source"
              description={t('entities:notificationChannelSourceHelp')}
            >
              <Input
                id="nc-source"
                value={draft.source}
                placeholder={SOURCE_PLACEHOLDER}
                onChange={(e) => set({ source: e.target.value })}
              />
            </FormField>
          </>
        )}
        {native && (
          <FormField
            label={t('entities:notificationChannelLinkBase')}
            htmlFor="nc-link"
            description={t('entities:notificationChannelLinkBaseHelp')}
          >
            <Input
              id="nc-link"
              value={draft.linkBaseUrl}
              placeholder={LINK_PLACEHOLDER}
              onChange={(e) => set({ linkBaseUrl: e.target.value })}
            />
          </FormField>
        )}
        {!native && (
          <FormField
            label={t('entities:notificationChannelConfig')}
            htmlFor="nc-config"
            description={t('entities:notificationChannelConfigHelp')}
          >
            <Textarea
              id="nc-config"
              className="font-mono text-xs"
              value={draft.rawConfig}
              onChange={(e) => set({ rawConfig: e.target.value })}
            />
          </FormField>
        )}
        {problem === 'secret' && (
          <HintText size="md">{t('entities:notificationChannelNeedsSecret')}</HintText>
        )}
        {problem === 'config' && (
          <p className="text-destructive text-xs">{t('entities:notificationChannelConfigInvalid')}</p>
        )}
      </fieldset>
      {canWrite && (
        <div className="flex gap-2">
          <Button
            onClick={submit}
            loading={busy}
            disabled={busy || problem !== null || (editing ? !dirty : !token.trim())}
          >
            {editing ? t('common:saveChanges') : t('entities:notificationChannelCreateAction')}
          </Button>
        </div>
      )}
    </div>
  );
}
