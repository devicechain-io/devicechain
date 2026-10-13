// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
import { EntityPicker, type EntityKind } from '@/routes/dashboards/editor/EntityPicker';
import { errMessage, useReload } from '@/routes/common';
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
import { formatTime } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import {
  REVISION_PAGE_SIZE,
  copyDeviceConfiguration,
  getDesiredConfigurationDocument,
  getDeviceConfigurationView,
  restoreDeviceConfiguration,
  type DeviceConfiguration,
  type DeviceConfigurationView,
} from '@/lib/api/device-configuration';
import { configurationBanner } from './configurationState';
import { diffConfigurationDocuments } from './configurationDiff';

// A change the operator has asked to preview: restoring one of this device's revisions, or
// copying another device's latest one. Nothing is written until the preview is applied.
type Proposal =
  | { kind: 'restore'; revision: number; document: string }
  | { kind: 'copy'; from: string; document: string };

// DeviceConfigurationPanel shows what a device is meant to be running and what it
// says it is running: the declared keys, the current desired document and revision,
// the device's last report, and the revision history. Values are edited in the Facets tab
// (shared attributes), which mints the next revision. With device:write it can also
// restore an earlier revision's content or copy another device's, each previewed key by
// key before it is applied; either mints a new revision and never changes an old one.
//
// 🔴 STALE-DATA GUARD. useQuery keeps the previous result while a refetch for new
// deps is in flight. When the operator moves from one device to another the old
// device's configuration would sit on screen under the new device's heading. Every
// result is stamped with the device it was read for, and is rendered only while that
// stamp equals the device on screen; until then the panel shows the loading state.
//
// 🔴 `pending` and `stale` come from the server and are never recomputed here.
// canWrite offers restore and copy. They write the device's shared attributes, so the page
// passes it only to a caller holding device:write; a reader sees the history without them.
export function DeviceConfigurationPanel({
  deviceToken,
  canWrite = false,
}: {
  deviceToken: string;
  canWrite?: boolean;
}) {
  const { t } = useTranslation('devices');
  const [version, reload] = useReload();
  const [proposal, setProposal] = useState<Proposal | null>(null);
  const { data, loading, error } = useQuery(
    () => getDeviceConfigurationView(deviceToken),
    [deviceToken, version],
  );

  // Data for another device is no data at all for this one - including when the
  // refetch for THIS device failed and useQuery kept the old data alongside the error.
  const view: DeviceConfigurationView | null =
    data && data.deviceToken === deviceToken ? data : null;

  if (error) return <ErrorState description={error} />;
  if (!view) return <LoadingState description={t('loadingConfiguration')} />;

  const cfg = view.configuration;
  if (!cfg) return <EmptyState description={t('configurationDeviceNotFound')} />;

  const proposeCopy = (from: string, document: string) => setProposal({ kind: 'copy', from, document });
  const proposeRestore = (r: { revision: number; document: string }) =>
    setProposal({ kind: 'restore', revision: r.revision, document: r.document });

  return (
    <div className="space-y-6">
      <ConfigurationStatus cfg={cfg} loading={loading} />
      <DeclaredKeys view={view} cfg={cfg} />
      <DesiredDocument cfg={cfg} />
      {canWrite && (
        <CopyFromDevice
          deviceToken={deviceToken}
          onPreview={proposeCopy}
        />
      )}
      {proposal && (
        <ChangePreview
          deviceToken={deviceToken}
          current={cfg.desired?.document ?? null}
          proposal={proposal}
          onClose={() => setProposal(null)}
          onApplied={() => {
            setProposal(null);
            reload();
          }}
        />
      )}
      <RevisionHistory
        view={view}
        current={cfg.desired?.revision ?? null}
        onRestore={canWrite ? proposeRestore : undefined}
      />
    </div>
  );
}

// The copy source is always a device: the picker lists this tenant's devices.
const SOURCE_KIND: EntityKind = 'device';

// CopyFromDevice picks another device and previews copying its latest revision here.
function CopyFromDevice({
  deviceToken,
  onPreview,
}: {
  deviceToken: string;
  onPreview: (from: string, document: string) => void;
}) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const [from, setFrom] = useState('');
  const [busy, setBusy] = useState(false);

  const preview = async () => {
    setBusy(true);
    try {
      const document = await getDesiredConfigurationDocument(from);
      if (document === null) {
        toast(t('configCopyNoRevision', { token: from }), 'error');
        return;
      }
      onPreview(from, document);
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium text-foreground">{t('configCopyTitle')}</h3>
      <p className="text-xs text-muted-foreground">{t('configCopyHint')}</p>
      <div className="flex flex-wrap items-center gap-2">
        <div className="min-w-64">
          <EntityPicker kind={SOURCE_KIND} value={from} onChange={setFrom} id="config-copy-from" />
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={preview}
          disabled={!from || from === deviceToken || busy}
        >
          {t('configPreviewChanges')}
        </Button>
      </div>
    </section>
  );
}

// ChangePreview shows, key by key, what applying a proposal would change, and applies it
// only when the operator says so. A key whose value does not change is listed too, so the
// operator sees the whole document the device will hold.
function ChangePreview({
  deviceToken,
  current,
  proposal,
  onClose,
  onApplied,
}: {
  deviceToken: string;
  current: string | null;
  proposal: Proposal;
  onClose: () => void;
  onApplied: () => void;
}) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const changes = diffConfigurationDocuments(current, proposal.document);
  const changed = changes.filter((c) => c.kind !== 'unchanged').length;

  const apply = async () => {
    setBusy(true);
    try {
      const revision =
        proposal.kind === 'restore'
          ? await restoreDeviceConfiguration(deviceToken, proposal.revision)
          : await copyDeviceConfiguration(proposal.from, deviceToken);
      toast(t('configChangeApplied', { revision: revision ?? '—' }));
      onApplied();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="space-y-3 rounded-md border border-border p-3" aria-label={t('configPreviewTitle')}>
      <h3 className="text-sm font-medium text-foreground">
        {proposal.kind === 'restore'
          ? t('configPreviewRestoreTitle', { revision: proposal.revision })
          : t('configPreviewCopyTitle', { token: proposal.from })}
      </h3>
      <p className="text-xs text-muted-foreground">
        {changed === 0 ? t('configPreviewNoChanges') : t('configPreviewHint', { count: changed })}
      </p>
      {changes.length > 0 && (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('configKeyColumn')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('configPreviewCurrent')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('configPreviewProposed')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('configPreviewChange')}</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {changes.map((c) => (
              <DataTableRow key={c.key}>
                <DataTableCell className="font-mono text-xs">{c.key}</DataTableCell>
                <DataTableCell className="font-mono text-xs break-all text-muted-foreground">
                  {c.before ?? '—'}
                </DataTableCell>
                <DataTableCell className="font-mono text-xs break-all text-foreground">
                  {c.after ?? '—'}
                </DataTableCell>
                <DataTableCell>
                  {c.kind === 'added' && <Badge variant="success">{t('configChangeAdded')}</Badge>}
                  {c.kind === 'changed' && <Badge variant="warning">{t('configChangeChanged')}</Badge>}
                  {c.kind === 'removed' && <Badge variant="destructive">{t('configChangeRemoved')}</Badge>}
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={apply} disabled={busy || changed === 0}>
          {t('configApplyChange')}
        </Button>
        <Button size="sm" variant="outline" onClick={onClose} disabled={busy}>
          {t('common:cancel')}
        </Button>
      </div>
    </section>
  );
}

function ConfigurationStatus({ cfg, loading }: { cfg: DeviceConfiguration; loading: boolean }) {
  const { t } = useTranslation('devices');
  const banner = configurationBanner(cfg);
  const reported = cfg.reported;

  return (
    <div className="space-y-3" aria-busy={loading}>
      <div className="flex flex-wrap items-center gap-2">
        {banner === 'pending' && <Badge variant="warning">{t('configPending')}</Badge>}
        {banner === 'inSync' && <Badge variant="success">{t('configInSync')}</Badge>}
        {banner === 'noRevision' && <Badge variant="secondary">{t('configNoRevision')}</Badge>}
        {cfg.stale && <Badge variant="warning">{t('configStale')}</Badge>}
      </div>
      {banner === 'pending' && (
        <p className="text-sm text-muted-foreground">
          {reported?.revision != null
            ? t('configPendingBody', {
                reported: reported.revision,
                desired: cfg.desired?.revision,
              })
            : t('configPendingNeverReported', { desired: cfg.desired?.revision })}
        </p>
      )}
      {cfg.stale && <p className="text-sm text-muted-foreground">{t('configStaleBody')}</p>}

      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
        <dt className="text-muted-foreground">{t('configDesiredRevision')}</dt>
        <dd className="text-foreground">
          {cfg.desired ? (
            <>
              <span className="font-medium">#{cfg.desired.revision}</span>{' '}
              <span className="font-mono text-xs break-all text-muted-foreground">
                {cfg.desired.digest}
              </span>
            </>
          ) : (
            '—'
          )}
        </dd>
        <dt className="text-muted-foreground">{t('configReportedRevision')}</dt>
        <dd className="text-foreground">
          {reported?.revision != null ? (
            <>
              <span className="font-medium">#{reported.revision}</span>{' '}
              {reported.digest && (
                <span className="font-mono text-xs break-all text-muted-foreground">
                  {reported.digest}
                </span>
              )}
            </>
          ) : (
            t('configNeverReported')
          )}
        </dd>
        <dt className="text-muted-foreground">{t('configReportStatus')}</dt>
        <dd className="text-foreground">
          {reported?.status ? <ReportStatusBadge status={reported.status} /> : '—'}
        </dd>
        <dt className="text-muted-foreground">{t('configReportedAt')}</dt>
        <dd className="text-foreground">{formatTime(reported?.reportedAt)}</dd>
        <dt className="text-muted-foreground">{t('configLastSync')}</dt>
        <dd className="text-foreground">{formatTime(reported?.lastSyncAt)}</dd>
      </dl>

      {reported && reported.errors.length > 0 && (
        <div className="space-y-1 rounded-md border border-destructive/40 bg-destructive/5 p-3">
          <p className="text-sm font-medium text-destructive">{t('configReportErrors')}</p>
          <ul className="list-disc pl-5 text-sm text-foreground">
            {reported.errors.map((e, i) => (
              <li key={i}>{e}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function ReportStatusBadge({ status }: { status: string }) {
  const { t } = useTranslation('devices');
  switch (status) {
    case 'APPLIED':
      return <Badge variant="success">{t('configStatusApplied')}</Badge>;
    case 'RECEIVED':
      return <Badge variant="secondary">{t('configStatusReceived')}</Badge>;
    case 'REJECTED':
      return <Badge variant="destructive">{t('configStatusRejected')}</Badge>;
    default:
      // An unrecognised status is shown verbatim rather than worded as a known one.
      return <Badge variant="outline">{status}</Badge>;
  }
}

function DeclaredKeys({ view, cfg }: { view: DeviceConfigurationView; cfg: DeviceConfiguration }) {
  const { t } = useTranslation('devices');
  const keys = view.declaration;
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium text-foreground">{t('configDeclaredKeys')}</h3>
      {keys === null ? (
        <p className="text-sm text-muted-foreground">{t('configDeclarationUnavailable')}</p>
      ) : keys.length === 0 ? (
        <EmptyState description={t('configNoDeclaredKeys')} />
      ) : (
        <>
          <p className="text-xs text-muted-foreground">{t('configDeclaredKeysHint')}</p>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('configKeyColumn')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('configTypeColumn')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('configDescriptionColumn')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('configKeyStateColumn')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {keys.map((k) => (
                <DataTableRow key={k.key}>
                  <DataTableCell className="font-mono text-xs">{k.key}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{k.valueType}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {k.description || '—'}
                  </DataTableCell>
                  <DataTableCell>
                    {cfg.invalid.includes(k.key) && (
                      <Badge variant="destructive">{t('configKeyInvalid')}</Badge>
                    )}
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        </>
      )}
      {cfg.invalid.length > 0 && (
        <p className="text-xs text-muted-foreground">{t('configInvalidHint')}</p>
      )}
      {cfg.undeclared.length > 0 && (
        <div className="space-y-1">
          <p className="text-sm font-medium text-foreground">{t('configUndeclared')}</p>
          <p className="text-xs text-muted-foreground">{t('configUndeclaredHint')}</p>
          <div className="flex flex-wrap gap-1.5">
            {cfg.undeclared.map((k) => (
              <Badge key={k} variant="outline" className="font-mono">
                {k}
              </Badge>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}

// The document is shown as the exact bytes the digest covers, never re-serialized.
function DesiredDocument({ cfg }: { cfg: DeviceConfiguration }) {
  const { t } = useTranslation('devices');
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium text-foreground">{t('configDesiredDocument')}</h3>
      {cfg.desired ? (
        <pre className="overflow-x-auto rounded-md border border-border bg-muted/40 p-3 font-mono text-xs text-foreground">
          {cfg.desired.document}
        </pre>
      ) : (
        <EmptyState description={t('configNoRevisionBody')} />
      )}
    </section>
  );
}

// RevisionHistory lists the device's revisions. With onRestore, every revision but the
// current one offers a restore, which opens the preview rather than writing anything.
function RevisionHistory({
  view,
  current,
  onRestore,
}: {
  view: DeviceConfigurationView;
  current: number | null;
  onRestore?: (revision: DeviceConfigurationView['revisions'][number]) => void;
}) {
  const { t } = useTranslation('devices');
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium text-foreground">{t('configHistory')}</h3>
      {view.revisions.length === 0 ? (
        <EmptyState description={t('configNoHistory')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('configRevisionColumn')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('configDigestColumn')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('configCreatedColumn')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('configActorColumn')}</DataTableHeaderCell>
              {onRestore && <DataTableHeaderCell>{t('common:colActions')}</DataTableHeaderCell>}
            </DataTableHead>
            <DataTableBody>
              {view.revisions.map((r) => (
                <DataTableRow key={r.revision}>
                  <DataTableCell className="font-medium">#{r.revision}</DataTableCell>
                  <DataTableCell className="font-mono text-xs break-all text-muted-foreground">
                    {r.digest}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {formatTime(r.createdAt)}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{r.actor || '—'}</DataTableCell>
                  {onRestore && (
                    <DataTableCell>
                      {r.revision !== current && (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => onRestore(r)}
                          aria-label={t('configRestoreRevisionLabel', { revision: r.revision })}
                        >
                          {t('configRestore')}
                        </Button>
                      )}
                    </DataTableCell>
                  )}
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          {view.totalRevisions > REVISION_PAGE_SIZE && (
            <p className="text-xs text-muted-foreground">
              {t('configHistoryShowingLatest', { count: view.revisions.length })}
            </p>
          )}
        </>
      )}
    </section>
  );
}
