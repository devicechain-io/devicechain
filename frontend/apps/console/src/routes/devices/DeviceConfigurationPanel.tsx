// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
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
  getDeviceConfigurationView,
  type DeviceConfiguration,
  type DeviceConfigurationView,
} from '@/lib/api/device-configuration';
import { configurationBanner } from './configurationState';

// DeviceConfigurationPanel shows what a device is meant to be running and what it
// says it is running: the declared keys, the current desired document and revision,
// the device's last report, and the revision history. It is read-only; values are
// edited in the Facets tab (shared attributes), which mints the next revision.
//
// 🔴 STALE-DATA GUARD. useQuery keeps the previous result while a refetch for new
// deps is in flight. When the operator moves from one device to another the old
// device's configuration would sit on screen under the new device's heading. Every
// result is stamped with the device it was read for, and is rendered only while that
// stamp equals the device on screen; until then the panel shows the loading state.
//
// 🔴 `pending` and `stale` come from the server and are never recomputed here.
export function DeviceConfigurationPanel({ deviceToken }: { deviceToken: string }) {
  const { t } = useTranslation('devices');
  const { data, loading, error } = useQuery(
    () => getDeviceConfigurationView(deviceToken),
    [deviceToken],
  );

  // Data for another device is no data at all for this one - including when the
  // refetch for THIS device failed and useQuery kept the old data alongside the error.
  const view: DeviceConfigurationView | null =
    data && data.deviceToken === deviceToken ? data : null;

  if (error) return <ErrorState description={error} />;
  if (!view) return <LoadingState description={t('loadingConfiguration')} />;

  const cfg = view.configuration;
  if (!cfg) return <EmptyState description={t('configurationDeviceNotFound')} />;

  return (
    <div className="space-y-6">
      <ConfigurationStatus cfg={cfg} loading={loading} />
      <DeclaredKeys view={view} cfg={cfg} />
      <DesiredDocument cfg={cfg} />
      <RevisionHistory view={view} />
    </div>
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

function RevisionHistory({ view }: { view: DeviceConfigurationView }) {
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
