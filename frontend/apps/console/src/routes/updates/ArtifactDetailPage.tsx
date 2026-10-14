// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// One artifact: what it is, whether anyone has vouched for it, and the three things an
// operator does to it: upload its bytes (while it is still UPLOADING), sign it, retire it.
//
// 🔴 WHAT THIS PAGE DOES NOT SHOW, AND WHY. The service derives an artifact's trust from the
// signatures on record, but it exposes the RESULT (SIGNED / UNVERIFIED), not the signatures
// themselves: an artifact has no signature list in the API. So the page states the derived
// trust and lists the tenant's registered signers with their state (revoking one is what
// moves an artifact back to UNVERIFIED), and it says out loud that the per-artifact list is
// not available. It never invents one from the registry, which would claim that signers
// vouched for this artifact when it only knows they exist.
//
// Routed page: the AreaGate in App.tsx covers 'absent'. Write controls require the
// area to be known present AND artifact:write (useUpdateAccess().canWrite); the service
// re-authorizes every call regardless.

import { useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Archive, Upload } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { CopyToken } from '@/components/ui/copy-token';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { HintText } from '@/components/ui/hint-text';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { FilePicker } from '@/components/ui/file-picker';
import { useConfirm } from '@/components/ui/confirm-dialog';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { useQuery } from '@/lib/hooks/use-query';
import { useUpdateAccess } from '@/lib/updateAuthority';
import { errMessage, useReload } from '@/routes/common';
import { formatTime, parseJsonObject } from '@/lib/utils';
import {
  getArtifact,
  getUpdateManagementInfo,
  listArtifactSigners,
  retireArtifact,
  type Artifact,
} from '@/lib/api/update-management';
import { formatBytes, sha256Digest } from '@/lib/artifactUpload';
import { Fact } from '@/routes/command-batches/Fact';
import { ArtifactStateBadge, DigestChip, SignerStateBadge, TrustBadge } from './badges';
import { AddSignatureForm } from './AddSignatureForm';
import { UploadPanel, useArtifactUpload } from './useArtifactUpload';

// Where the signing runbook lives. Linked, not restated: the commands are tooling-specific
// and belong in one place that can change without a console release.
export const SIGNING_GUIDE_URL = 'https://docs.devicechain.io/guides/signing-updates';

export default function ArtifactDetailPage() {
  const { t } = useTranslation('updates');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = rawToken ?? '';
  const [reloadKey, reload] = useReload();
  const query = useQuery(() => getArtifact(token), [token, reloadKey]);
  const { loading, error } = query;
  // useQuery keeps the PREVIOUS data while a changed key is in flight (and after it fails).
  // A reload of the same artifact may show it; another artifact's data must not, or the page
  // would show artifact A, with A's Retire and signature controls, under artifact B's URL.
  const data = query.data && query.data.token === token ? query.data : null;

  // First-load gating only: useQuery keeps the prior data across a refetch.
  if (loading && !data) {
    return (
      <PageShell title={token} banner="devices">
        <LoadingState description={t('loadingArtifact')} />
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
        <ErrorState description={t('artifactNotFound', { token })} />
      </PageShell>
    );
  }
  // Keyed by token: per-artifact UI state (an upload in progress, a refusal) belongs to one artifact.
  return <ArtifactDetail key={data.token} artifact={data} reloadKey={reloadKey} onChanged={reload} />;
}

function ArtifactDetail({
  artifact,
  reloadKey,
  onChanged,
}: {
  artifact: Artifact;
  reloadKey: number;
  onChanged: () => void;
}) {
  const { t } = useTranslation('updates');
  const { canWrite } = useUpdateAccess();
  const confirm = useConfirm();
  const [retiring, setRetiring] = useState(false);
  const [retireError, setRetireError] = useState<string | null>(null);

  const retire = async () => {
    if (retiring) return;
    const ok = await confirm({
      title: t('retireConfirmTitle', { name: artifact.name, version: artifact.version }),
      description: t('retireConfirmBody'),
      confirmLabel: t('retire'),
    });
    if (!ok) return;
    setRetiring(true);
    setRetireError(null);
    try {
      await retireArtifact(artifact.token);
      onChanged();
    } catch (err) {
      setRetireError(errMessage(err));
    } finally {
      setRetiring(false);
    }
  };

  return (
    <PageShell
      title={`${artifact.name} ${artifact.version}`}
      titleAdornment={<CopyToken value={artifact.token} />}
      banner="devices"
      action={
        canWrite && artifact.state !== 'RETIRED' ? (
          <Button variant="destructive" onClick={retire} loading={retiring} disabled={retiring}>
            <Archive size={14} /> {t('retire')}
          </Button>
        ) : undefined
      }
      description={
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <TrustBadge status={artifact.trustStatus} />
          <ArtifactStateBadge state={artifact.state} />
          <Badge variant="outline">{artifact.kind}</Badge>
        </div>
      }
    >
      <div className="space-y-6">
        {retireError && <ErrorBanner message={retireError} onDismiss={() => setRetireError(null)} />}
        {artifact.trustStatus === 'UNVERIFIED' && <UnverifiedNotice />}
        <DetailsPanel artifact={artifact} />
        {artifact.state === 'UPLOADING' && canWrite && <UploadContentPanel artifact={artifact} onUploaded={onChanged} />}
        <SignaturesPanel artifact={artifact} reloadKey={reloadKey} canWrite={canWrite} onChanged={onChanged} />
      </div>
    </PageShell>
  );
}

// ── UNVERIFIED notice ──────────────────────────────────────────────────────

function UnverifiedNotice() {
  const { t } = useTranslation('updates');
  const info = useQuery(getUpdateManagementInfo, []);
  const guide = (
    <a className="underline" href={SIGNING_GUIDE_URL} target="_blank" rel="noopener noreferrer">
      {t('signingGuide')}
    </a>
  );
  // Three honest answers, because the policy is an instance setting this page reads and
  // can fail to read: do not state a rule that was not read.
  let body: React.ReactNode;
  if (info.data?.allowUnverifiedArtifacts === false) {
    body = t('unverifiedBlocked');
  } else if (info.data?.allowUnverifiedArtifacts === true) {
    body = t('unverifiedAllowed');
  } else if (info.loading) {
    return null;
  } else {
    body = t('unverifiedPolicyUnknown');
  }
  return (
    <div
      role="status"
      data-testid="unverified-notice"
      className="rounded-md border border-warning/40 bg-warning/10 p-4 text-sm text-foreground"
    >
      <p className="font-medium">{t('unverifiedTitle')}</p>
      <p className="mt-1">{body}</p>
      <p className="mt-1">
        {t('unverifiedHowTo')} {guide}
      </p>
    </div>
  );
}

// ── Details ────────────────────────────────────────────────────────────────

function DetailsPanel({ artifact }: { artifact: Artifact }) {
  const { t } = useTranslation('updates');
  const provenance = useMemo(() => parseJsonObject(artifact.provenance), [artifact.provenance]);
  const provEntries = Object.entries(provenance).filter(([, v]) => v != null && v !== '');
  return (
    <SectionPanel title={t('detailsTitle')}>
      <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Fact label={t('colComponent')}>{artifact.component}</Fact>
        <Fact label={t('colVersion')}>{artifact.version}</Fact>
        <Fact label={t('colSize')}>
          <span title={t('exactBytes', { bytes: artifact.sizeBytes })}>{formatBytes(artifact.sizeBytes)}</span>
        </Fact>
        <Fact label={t('fieldMediaType')}>{artifact.mediaType}</Fact>
        <Fact label={t('colDigest')}>
          <DigestChip digest={artifact.digest} />
        </Fact>
        <Fact label={t('factDeclared')}>{formatTime(artifact.createdAt)}</Fact>
        <Fact label={t('factChanged')}>{formatTime(artifact.updatedAt)}</Fact>
        <Fact label={t('fieldProfiles')}>
          <div className="flex flex-wrap gap-1">
            {artifact.compatibleProfiles.map((p) => (
              <Badge key={p} variant="outline">
                {p}
              </Badge>
            ))}
          </div>
        </Fact>
      </dl>
      {provEntries.length > 0 && (
        <dl className="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {provEntries.map(([k, v]) => (
            <Fact key={k} label={k}>
              {typeof v === 'string' ? v : JSON.stringify(v)}
            </Fact>
          ))}
        </dl>
      )}
    </SectionPanel>
  );
}

// ── Upload (UPLOADING artifacts) ───────────────────────────────────────────

function UploadContentPanel({ artifact, onUploaded }: { artifact: Artifact; onUploaded: () => void }) {
  const { t } = useTranslation('updates');
  const expected = useMemo(
    () => ({ sizeBytes: artifact.sizeBytes, digest: artifact.digest }),
    [artifact.sizeBytes, artifact.digest],
  );
  const upload = useArtifactUpload(artifact.token, expected);
  const [refusal, setRefusal] = useState<string | null>(null);

  const pick = async (file: File) => {
    setRefusal(null);
    // Refused locally, before the first byte is sent, when the file cannot be the declared
    // one. The service would refuse it too; this saves moving the bytes to learn that.
    if (String(file.size) !== artifact.sizeBytes) {
      setRefusal(t('uploadSizeMismatch', { size: file.size, declared: artifact.sizeBytes }));
      return;
    }
    try {
      if ((await sha256Digest(file)) !== artifact.digest) {
        setRefusal(t('uploadDigestMismatch'));
        return;
      }
    } catch (err) {
      setRefusal(t('hashFailed', { error: errMessage(err) }));
      return;
    }
    if (await upload.start(file)) onUploaded();
  };

  return (
    <SectionPanel title={t('uploadContentTitle')} description={t('uploadContentBody')}>
      <FilePicker onPick={pick} disabled={upload.phase === 'uploading'}>
        <Upload size={14} /> {t('chooseFile')}
      </FilePicker>
      {refusal && <p className="mt-2 text-sm text-destructive">{refusal}</p>}
      <div className="mt-3">
        <UploadPanel state={upload} expected={expected} />
      </div>
    </SectionPanel>
  );
}

// ── Signatures ─────────────────────────────────────────────────────────────

function SignaturesPanel({
  artifact,
  reloadKey,
  canWrite,
  onChanged,
}: {
  artifact: Artifact;
  reloadKey: number;
  canWrite: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('updates');
  // Keyed by reloadKey: adding a signature changes nothing in this list, but a signer
  // revoked elsewhere does, and a stale state badge would be a wrong answer.
  const signers = useQuery(() => listArtifactSigners({ pageNumber: 1, pageSize: 1000 }), [reloadKey]);
  return (
    <SectionPanel title={t('signaturesTitle')} description={t('signaturesDescription')}>
      <div className="flex flex-wrap items-center gap-2">
        <TrustBadge status={artifact.trustStatus} />
        <span className="text-sm text-muted-foreground">
          {artifact.trustStatus === 'SIGNED' ? t('trustSignedExplain') : t('trustUnverifiedExplain')}
        </span>
      </div>
      <HintText size="md" className="mt-2">
        {t('signaturesNotListed')}
      </HintText>

      <h3 className="mt-5 mb-2 text-sm font-semibold text-foreground">{t('registeredSigners')}</h3>
      {signers.loading && !signers.data ? (
        <LoadingState description={t('loadingSigners')} />
      ) : signers.error && !signers.data ? (
        <ErrorState description={signers.error} />
      ) : (signers.data?.results.length ?? 0) === 0 ? (
        <HintText size="md">{t('noSigners')}</HintText>
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('colName')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colAlgorithm')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colKeyId')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colState')}</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {signers.data!.results.map((s) => (
              <DataTableRow key={s.id}>
                <DataTableCell className="font-medium text-foreground">{s.name}</DataTableCell>
                <DataTableCell className="text-muted-foreground">{s.algorithm}</DataTableCell>
                <DataTableCell>
                  <CopyToken value={s.keyId} display={`${s.keyId.slice(0, 12)}…`} />
                </DataTableCell>
                <DataTableCell>
                  <SignerStateBadge state={s.state} />
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}

      {canWrite && (
        <div className="mt-6 border-t border-border pt-4">
          <h3 className="mb-3 text-sm font-semibold text-foreground">{t('addSignatureTitle')}</h3>
          <AddSignatureForm artifactToken={artifact.token} reloadKey={reloadKey} onAdded={onChanged} />
        </div>
      )}
    </SectionPanel>
  );
}
