// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Combobox } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { Textarea } from '@/components/ui/textarea';
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
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { formatTime } from '@/lib/utils';
import { signerStateLabel, updateStateLabel } from '@/lib/enumLabel';
import { useAuth } from '@/auth/AuthProvider';
import { isScoped } from '@devicechain/client';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { DownloadsNoticeView } from '@/routes/updates/DownloadsNotice';
import {
  abandonUnknownUpdate,
  assignArtifact,
  cancelUpdate,
  getUpdateManagementInfo,
  listArtifacts,
  type ArtifactTrustStatus,
  type UpdateState,
} from '@/lib/api/update-management';
import {
  getAssignmentSigner,
  listDeviceUpdateAssignments,
  type DeviceUpdateAssignment,
} from '@/lib/api/device-updates';

// The docs the tab points at rather than repeating.
const SIGNING_GUIDE_URL = 'https://docs.devicechain.io/guides/signing-updates';
const SIGNERS_CONCEPT_URL = 'https://docs.devicechain.io/concepts/over-the-air-updates#signers';

const HISTORY_PAGE_SIZE = 10;
// A device has one live target per component, so this is far above any real count.
const CURRENT_PAGE_SIZE = 50;
const ARTIFACT_PICK_LIMIT = 100;

// Before installation begins an update can still be recalled; from UPDATING on it cannot. A
// QUEUED update is cancelled at once, the others record a request the device must confirm.
const CANCELLABLE: readonly UpdateState[] = ['QUEUED', 'INITIATED', 'DOWNLOADING', 'DOWNLOADED', 'VERIFIED'];

// The failure codes an operator can act on. Anything else is shown as reported: the code and
// the device's own detail.
const EXPLAINED_FAILURE_CODES = ['SIGNATURE_INVALID', 'UNKNOWN_SIGNER', 'VERIFICATION_FAILED'] as const;

const isFinished = (s: UpdateState) => s === 'UPDATED' || s === 'FAILED' || s === 'TIMED_OUT' || s === 'CANCELLED';

export function stateVariant(
  state: UpdateState,
): 'default' | 'secondary' | 'success' | 'destructive' | 'warning' | 'outline' {
  switch (state) {
    case 'UPDATED':
      return 'success';
    case 'FAILED':
    case 'TIMED_OUT':
      return 'destructive';
    case 'UNKNOWN':
      return 'warning';
    case 'CANCELLED':
      return 'secondary';
    default:
      return 'default';
  }
}

export function TrustBadge({ status }: { status: ArtifactTrustStatus }) {
  const { t } = useTranslation('devices');
  return status === 'SIGNED' ? (
    <Badge variant="success">{t('updTrustSigned')}</Badge>
  ) : (
    <Badge variant="warning">{t('updTrustUnverified')}</Badge>
  );
}

// Progress is two decimal STRINGS (a byte count can exceed 32 bits); parse them as BigInt and
// never let them coerce to a lossy number. A total of 0 means the device did not say.
export function progressText(p: { bytes: string; total: string } | null | undefined): string | null {
  if (!p) return null;
  try {
    const bytes = BigInt(p.bytes);
    const total = BigInt(p.total);
    if (total <= BigInt(0)) return `${bytes.toString()} B`;
    const pct = Number((bytes * BigInt(1000)) / total) / 10;
    return `${bytes.toString()} / ${total.toString()} B (${Math.min(pct, 100).toFixed(1)}%)`;
  } catch {
    return `${p.bytes} / ${p.total} B`;
  }
}

// DeviceUpdatesPanel is a device's view of the opt-in OTA area: what it is being moved to
// now, how that is going, why it failed, the history, and the operator's controls.
//
// 🔴 THE CALLER GATES IT. DeviceDetailPage mounts the tab only when the area is known to be
// deployed and the caller may read it (useUpdateAccess().canRead); `canWrite` is
// useUpdateAccess().canWrite. Both are presentation: the service re-authorizes every call.
//
// 🔴 A DISABLED DEVICE IS CUT OFF FROM UPDATES. The server refuses an assignment to it
// (DEVICE_DISABLED); the notice and the disabled Assign say so up front rather than after a
// round trip. Cancel and Abandon stay available: ending an update is not delivering one.
//
// 🔴 EVERY READ FAILURE IS AN ERROR, NEVER AN EMPTY STATE. An empty history that was really a
// failed read would tell the operator nothing is happening on a device that is mid-update.
export function DeviceUpdatesPanel({
  deviceToken,
  deviceEnabled,
  canWrite,
}: {
  deviceToken: string;
  deviceEnabled: boolean;
  canWrite: boolean;
}) {
  const { t } = useTranslation('devices');
  const [version, reload] = useReload();
  const [pageNumber, setPageNumber] = useState(1);

  // A member limited to part of the tenant reads its devices' assignments but not who made
  // them; asking for that field would fail the whole list.
  const { claims } = useAuth();
  const scoped = isScoped(claims);

  const current = useQuery(
    () =>
      listDeviceUpdateAssignments(
        { deviceToken, currentOnly: true, pageNumber: 1, pageSize: CURRENT_PAGE_SIZE },
        { scoped },
      ),
    [deviceToken, version, scoped],
  );
  const history = useQuery(
    () => listDeviceUpdateAssignments({ deviceToken, pageNumber, pageSize: HISTORY_PAGE_SIZE }, { scoped }),
    [deviceToken, pageNumber, version, scoped],
  );

  const afterChange = () => {
    setPageNumber(1);
    reload();
  };

  return (
    <div className="space-y-6">
      {!deviceEnabled && (
        <div
          role="status"
          data-testid="updates-device-disabled"
          className="space-y-0.5 rounded-md border border-warning/40 bg-warning/10 px-4 py-3 text-sm"
        >
          <div className="font-medium text-foreground">{t('updDeviceDisabled')}</div>
          <div className="text-muted-foreground">{t('updDeviceDisabledHint')}</div>
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">
          {t('updDocsLead')}{' '}
          <a className="text-primary underline" href={SIGNING_GUIDE_URL} target="_blank" rel="noreferrer">
            {t('updDocsSigning')}
          </a>
          {' · '}
          <a className="text-primary underline" href={SIGNERS_CONCEPT_URL} target="_blank" rel="noreferrer">
            {t('updDocsSigners')}
          </a>
        </p>
        <Button variant="outline" size="sm" onClick={reload}>
          <RefreshCw size={14} /> {t('updRefresh')}
        </Button>
      </div>

      <section className="space-y-3" aria-label={t('updCurrentTitle')}>
        <h3 className="text-sm font-semibold text-foreground">{t('updCurrentTitle')}</h3>
        {current.loading && !current.data ? (
          <LoadingState description={t('updLoading')} />
        ) : current.error && !current.data ? (
          <ErrorState description={current.error} />
        ) : (current.data?.results.length ?? 0) === 0 ? (
          <EmptyState description={t('updNoCurrent')} />
        ) : (
          current.data!.results.map((a) => (
            <CurrentAssignment key={a.token} assignment={a} canWrite={canWrite} onChanged={afterChange} />
          ))
        )}
      </section>

      {canWrite && (
        <AssignForm
          deviceToken={deviceToken}
          deviceEnabled={deviceEnabled}
          hasLiveUpdate={(current.data?.results ?? []).some((a) => !isFinished(a.attempt.state))}
          onAssigned={afterChange}
        />
      )}

      <section className="space-y-3" aria-label={t('updHistoryTitle')}>
        <h3 className="text-sm font-semibold text-foreground">{t('updHistoryTitle')}</h3>
        {history.loading && !history.data ? (
          <LoadingState description={t('updLoading')} />
        ) : history.error && !history.data ? (
          <ErrorState description={history.error} />
        ) : (history.data?.results.length ?? 0) === 0 ? (
          <EmptyState description={t('updNoHistory')} />
        ) : (
          <>
            <DataTable>
              <DataTableHead>
                <DataTableHeaderCell>{t('updColArtifact')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('updColState')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('updColFailure')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('updColAssigned')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('updColActor')}</DataTableHeaderCell>
              </DataTableHead>
              <DataTableBody>
                {history.data!.results.map((a) => (
                  <DataTableRow key={a.token}>
                    <DataTableCell>
                      <div className="text-sm text-foreground">
                        {a.artifact.name} <span className="font-mono text-xs">{a.artifact.version}</span>
                      </div>
                      <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
                        {a.component}
                        {a.current && <Badge variant="outline">{t('updCurrentBadge')}</Badge>}
                      </div>
                    </DataTableCell>
                    <DataTableCell>
                      <Badge variant={stateVariant(a.attempt.state)}>{updateStateLabel(t, a.attempt.state)}</Badge>
                    </DataTableCell>
                    <DataTableCell>
                      {a.attempt.failure ? (
                        <span className="font-mono text-xs">{a.attempt.failure.code}</span>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </DataTableCell>
                    <DataTableCell>{formatTime(a.createdAt)}</DataTableCell>
                    <DataTableCell>{a.actor || '—'}</DataTableCell>
                  </DataTableRow>
                ))}
              </DataTableBody>
            </DataTable>
            <Pagination
              pageNumber={pageNumber}
              pageSize={HISTORY_PAGE_SIZE}
              pagination={history.data!.pagination}
              onPageChange={setPageNumber}
            />
          </>
        )}
      </section>
    </div>
  );
}

// One current assignment: the artifact, the attempt's state and progress, the failure with its
// explanation, the pinned signer, and the controls its state allows.
function CurrentAssignment({
  assignment: a,
  canWrite,
  onChanged,
}: {
  assignment: DeviceUpdateAssignment;
  canWrite: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [abandoning, setAbandoning] = useState(false);
  const [reason, setReason] = useState('');
  const { attempt } = a;
  const progress = progressText(attempt.progress);
  const canCancel = CANCELLABLE.includes(attempt.state) && !attempt.cancelRequested;
  const beyondRecall = attempt.state === 'UPDATING' || attempt.state === 'REBOOTING';

  const cancel = async () => {
    const queued = attempt.state === 'QUEUED';
    const ok = await confirm({
      title: t('updCancelTitle'),
      description: queued ? t('updCancelConfirmQueued') : t('updCancelConfirmRequested'),
      confirmLabel: t('updCancelAction'),
      cancelLabel: t('updKeep'),
    });
    if (!ok) return;
    setBusy(true);
    try {
      await cancelUpdate(a.token);
      toast(queued ? t('updCancelled') : t('updCancelRequested'));
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  const abandon = async () => {
    const trimmed = reason.trim();
    if (!trimmed) {
      toast(t('updReasonRequired'), 'error');
      return;
    }
    const ok = await confirm({
      title: t('updAbandonTitle'),
      description: t('updAbandonConfirm'),
      confirmLabel: t('updAbandonAction'),
      cancelLabel: t('updKeep'),
    });
    if (!ok) return;
    setBusy(true);
    try {
      await abandonUnknownUpdate({ assignmentToken: a.token, reason: trimmed });
      toast(t('updAbandoned'));
      setAbandoning(false);
      setReason('');
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-3 rounded-lg border border-border p-4" data-testid="current-assignment">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium text-foreground">{a.artifact.name}</span>
        <span className="font-mono text-xs text-foreground">{a.artifact.version}</span>
        <TrustBadge status={a.artifact.trustStatus} />
        <Badge variant={stateVariant(attempt.state)}>{updateStateLabel(t, attempt.state)}</Badge>
        {attempt.cancelRequested && <Badge variant="warning">{t('updCancelPending')}</Badge>}
      </div>

      <dl className="grid grid-cols-1 gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
        <Fact label={t('updComponent')} value={a.component} />
        <Fact label={t('updAttempt')} value={attempt.token} mono />
        <Fact label={t('updProgress')} value={progress ?? t('updNoProgress')} />
        <Fact label={t('updLastReport')} value={attempt.lastReportAt ? formatTime(attempt.lastReportAt) : '—'} />
        <Fact label={t('updAssignedBy')} value={a.actor || '—'} />
        <Fact label={t('updRequiresReboot')} value={attempt.requiresReboot ? t('updYes') : t('updNo')} />
      </dl>

      <PinnedSigner assignmentToken={a.token} />

      {attempt.failure && <FailureNotice failure={attempt.failure} />}

      {attempt.state === 'UNKNOWN' && (
        <div role="status" className="rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm">
          {t('updUnknownExplain')}
        </div>
      )}
      {beyondRecall && <p className="text-xs text-muted-foreground">{t('updBeyondRecall')}</p>}

      {canWrite && (canCancel || attempt.state === 'UNKNOWN') && (
        <div className="space-y-3">
          {canCancel && (
            <Button variant="outline" size="sm" loading={busy} disabled={busy} onClick={cancel}>
              {t('updCancelAction')}
            </Button>
          )}
          {attempt.state === 'UNKNOWN' && !abandoning && (
            <Button variant="outline" size="sm" onClick={() => setAbandoning(true)}>
              {t('updAbandonAction')}
            </Button>
          )}
          {attempt.state === 'UNKNOWN' && abandoning && (
            <div className="space-y-2 rounded-md border border-border bg-muted/40 p-3">
              <FormField label={t('updReasonLabel')} htmlFor={`abandon-reason-${a.token}`} description={t('updReasonHint')}>
                <Textarea
                  id={`abandon-reason-${a.token}`}
                  className="font-sans"
                  rows={2}
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                />
              </FormField>
              <div className="flex gap-2">
                <Button variant="destructive" size="sm" loading={busy} disabled={busy} onClick={abandon}>
                  {t('updAbandonSubmit')}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={() => {
                    setAbandoning(false);
                    setReason('');
                  }}
                >
                  {t('updKeep')}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function Fact({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? 'font-mono text-xs break-all text-foreground' : 'text-foreground'}>{value}</dd>
    </div>
  );
}

// The signer an assignment is pinned to, read on its own so that an unreadable pin is shown
// HERE as an error. Null means the assignment pins no signature (made under the instance's
// allow-unverified setting with no active signer), which is a different thing and is said so.
function PinnedSigner({ assignmentToken }: { assignmentToken: string }) {
  const { t } = useTranslation('devices');
  const { data, loading, error } = useQuery(() => getAssignmentSigner(assignmentToken), [assignmentToken]);
  return (
    <div className="text-sm" data-testid="pinned-signer">
      <span className="text-muted-foreground">{t('updSignedBy')} </span>
      {error ? (
        <span role="alert" className="text-destructive">
          {t('updSignerError', { message: error })}
        </span>
      ) : loading ? (
        <span className="text-muted-foreground">{t('updLoading')}</span>
      ) : data ? (
        <span>
          <span className="text-foreground">{data.name}</span>{' '}
          <span className="font-mono text-xs break-all">{data.keyId}</span>{' '}
          <Badge variant={data.state === 'ACTIVE' ? 'success' : 'destructive'}>{signerStateLabel(t, data.state)}</Badge>
          {data.state === 'REVOKED' && (
            <span className="ml-1 text-xs text-muted-foreground">{t('updSignerRevokedNote')}</span>
          )}
        </span>
      ) : (
        <span className="text-muted-foreground">{t('updNoPin')}</span>
      )}
    </div>
  );
}

function FailureNotice({
  failure,
}: {
  failure: NonNullable<DeviceUpdateAssignment['attempt']['failure']>;
}) {
  const { t } = useTranslation('devices');
  const explained = (EXPLAINED_FAILURE_CODES as readonly string[]).includes(failure.code);
  return (
    <div role="alert" className="space-y-1 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm">
      <div className="font-medium text-foreground">
        {t('updFailureTitle')}: <span className="font-mono text-xs">{failure.code}</span>
        {failure.stage ? (
          <span className="ml-2 text-xs text-muted-foreground">{t('updFailureStage', { stage: failure.stage })}</span>
        ) : null}
      </div>
      {explained && <div className="text-foreground">{t(`updFailureExplain_${failure.code}`)}</div>}
      {failure.detail && <div className="text-muted-foreground">{failure.detail}</div>}
      <div className="text-xs text-muted-foreground">
        {failure.platform ? t('updFailureByPlatform') : t('updFailureByDevice')}
      </div>
    </div>
  );
}

// Assign: pick an AVAILABLE artifact and tell the device to install it. Whether an UNVERIFIED
// artifact may be assigned is the INSTANCE's setting; when it is off the option is marked and
// Assign is disabled with the reason. The server decides regardless, and a refusal (profile not
// compatible, an update already live, device disabled, ...) is shown exactly as it sends it.
function AssignForm({
  deviceToken,
  deviceEnabled,
  hasLiveUpdate,
  onAssigned,
}: {
  deviceToken: string;
  deviceEnabled: boolean;
  hasLiveUpdate: boolean;
  onAssigned: () => void;
}) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const [selected, setSelected] = useState('');
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);

  const info = useQuery(() => getUpdateManagementInfo(), []);
  const artifacts = useQuery(
    () => listArtifacts({ pageNumber: 1, pageSize: ARTIFACT_PICK_LIMIT, state: 'AVAILABLE' }),
    [],
  );

  // Unknown (the read failed or is pending) is NOT "not allowed": the server still decides.
  const unverifiedBlocked = info.data ? !info.data.allowUnverifiedArtifacts : false;
  const blocked = (status: ArtifactTrustStatus) => unverifiedBlocked && status === 'UNVERIFIED';

  const list = artifacts.data?.results ?? [];
  const chosen = list.find((a) => a.token === selected);
  const chosenBlocked = chosen ? blocked(chosen.trustStatus) : false;

  const options = list.map((a) => ({
    value: a.token,
    label: `${a.name} ${a.version}`,
    description: [
      a.trustStatus === 'SIGNED' ? t('updTrustSigned') : t('updTrustUnverified'),
      blocked(a.trustStatus) ? t('updNotAssignable') : null,
      a.component,
    ]
      .filter(Boolean)
      .join(' · '),
  }));

  const assign = async () => {
    if (!chosen) return;
    setBusy(true);
    setRefusal(null);
    try {
      await assignArtifact({ deviceToken, artifactToken: chosen.token });
      toast(t('updAssigned'));
      setSelected('');
      onAssigned();
    } catch (err) {
      setRefusal(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="space-y-3 rounded-lg border border-border bg-muted/40 p-4" aria-label={t('updAssignTitle')}>
      <h3 className="text-sm font-semibold text-foreground">{t('updAssignTitle')}</h3>
      <DownloadsNoticeView downloadsConfigured={info.data?.downloadsConfigured} />
      {artifacts.error && !artifacts.data ? (
        <ErrorState description={artifacts.error} />
      ) : (
        <div className="max-w-xl space-y-3">
          <FormField label={t('updArtifactLabel')} htmlFor="update-artifact" description={t('updArtifactHint')}>
            <Combobox
              id="update-artifact"
              options={options}
              value={selected}
              onChange={(v) => {
                setSelected(v);
                setRefusal(null);
              }}
              placeholder={artifacts.loading ? t('updLoading') : t('updArtifactPlaceholder')}
              emptyMessage={t('updNoAvailable')}
              disabled={!deviceEnabled}
            />
          </FormField>
          {artifacts.data && (artifacts.data.pagination.totalRecords ?? 0) > list.length && (
            <p className="text-xs text-muted-foreground">
              {t('updPickerTruncated', { shown: list.length, total: artifacts.data.pagination.totalRecords ?? 0 })}
            </p>
          )}
          {chosen && (
            <div className="flex items-center gap-2 text-sm">
              <TrustBadge status={chosen.trustStatus} />
              <span className="text-muted-foreground">{chosen.component}</span>
            </div>
          )}
          {chosenBlocked && (
            <p role="status" data-testid="assign-unverified-blocked" className="text-sm text-warning">
              {t('updUnverifiedBlocked')}
            </p>
          )}
          {hasLiveUpdate && <p className="text-xs text-muted-foreground">{t('updLiveBlocksAssign')}</p>}
          {refusal && (
            <div
              role="alert"
              data-testid="assign-refusal"
              className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-foreground"
            >
              {refusal}
            </div>
          )}
          <Button onClick={assign} loading={busy} disabled={busy || !chosen || chosenBlocked || !deviceEnabled}>
            {t('updAssignAction')}
          </Button>
          {!deviceEnabled && <p className="text-xs text-muted-foreground">{t('updAssignDisabledHint')}</p>}
        </div>
      )}
    </section>
  );
}
