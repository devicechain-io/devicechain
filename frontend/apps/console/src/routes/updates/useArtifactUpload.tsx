// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The upload half of an artifact, as a hook plus the panel that renders it. It is shared by
// the "New artifact" drawer (which declares and then uploads) and by the detail page of an
// artifact still in UPLOADING (whose first upload failed or was never started).
//
// WHAT "DONE" MEANS HERE. A 2xx from the content endpoint says the service stored the bytes
// and that they matched the declaration, and it echoes the size and digest it judged. The
// panel therefore shows the echoed values next to the declared ones rather than a bare
// "success": the verification result is what the operator came for, and an echo that
// disagrees with the declaration is rendered as a failure even though the status was 2xx.

import { useCallback, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { CheckCircle2, XCircle } from 'lucide-react';
import { ErrorBanner } from '@/components/ui/error-banner';
import { HintText } from '@/components/ui/hint-text';
import { uploadArtifactContent, formatBytes, type ArtifactUploadResult } from '@/lib/artifactUpload';
import { artifactStateLabel } from '@/lib/enumLabel';
import { errMessage } from '@/routes/common';
import { shortDigest } from './badges';
import '@/i18n/lazy/updates';

export interface UploadExpectation {
  sizeBytes: string;
  digest: string;
}

export type UploadPhase = 'idle' | 'uploading' | 'done' | 'error';

export interface ArtifactUploadState {
  phase: UploadPhase;
  loaded: number;
  total: number;
  result: ArtifactUploadResult | null;
  error: string | null;
  /** Upload `file` to the artifact's content endpoint. Resolves true when it was stored AND verified. */
  start: (file: File) => Promise<boolean>;
  reset: () => void;
}

/** True when the service's echo agrees with what was declared. */
export function echoMatches(result: ArtifactUploadResult, expected: UploadExpectation): boolean {
  return result.state === 'AVAILABLE' && result.sizeBytes === expected.sizeBytes && result.digest === expected.digest;
}

export function useArtifactUpload(token: string, expected: UploadExpectation): ArtifactUploadState {
  const [phase, setPhase] = useState<UploadPhase>('idle');
  const [progress, setProgress] = useState({ loaded: 0, total: 0 });
  const [result, setResult] = useState<ArtifactUploadResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  // A synchronous guard: state lands on the next render, and a second PUT of the same
  // artifact would only be refused by the service's per-artifact claim.
  const inFlight = useRef(false);

  const start = useCallback(
    async (file: File) => {
      if (inFlight.current) return false;
      inFlight.current = true;
      setPhase('uploading');
      setProgress({ loaded: 0, total: file.size });
      setResult(null);
      setError(null);
      try {
        const res = await uploadArtifactContent(token, file, {
          onProgress: (loaded, total) => setProgress({ loaded, total }),
        });
        setResult(res);
        if (!echoMatches(res, expected)) {
          // Fail closed: a success that does not describe the declared artifact is not one.
          setError(null);
          setPhase('error');
          return false;
        }
        setPhase('done');
        return true;
      } catch (err) {
        setError(errMessage(err));
        setPhase('error');
        return false;
      } finally {
        inFlight.current = false;
      }
    },
    [token, expected],
  );

  const reset = useCallback(() => {
    setPhase('idle');
    setResult(null);
    setError(null);
  }, []);

  return { phase, loaded: progress.loaded, total: progress.total, result, error, start, reset };
}

export function UploadPanel({ state, expected }: { state: ArtifactUploadState; expected: UploadExpectation }) {
  const { t } = useTranslation('updates');
  if (state.phase === 'idle') return null;
  const pct = state.total > 0 ? Math.min(100, Math.round((state.loaded / state.total) * 100)) : 0;
  return (
    <div className="space-y-3" data-testid="upload-panel">
      {state.phase === 'uploading' && (
        <div>
          <div
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={pct}
            aria-label={t('uploadProgressLabel')}
            className="h-2 w-full overflow-hidden rounded-full bg-muted"
          >
            <div className="h-full bg-primary transition-[width]" style={{ width: `${pct}%` }} />
          </div>
          <HintText size="md" className="mt-1">
            {t('uploadProgress', { loaded: formatBytes(state.loaded), total: formatBytes(state.total), pct })}
          </HintText>
        </div>
      )}
      {state.error && <ErrorBanner message={state.error} />}
      {state.result && (
        <Verification result={state.result} expected={expected} ok={echoMatches(state.result, expected)} />
      )}
    </div>
  );
}

function Verification({
  result,
  expected,
  ok,
}: {
  result: ArtifactUploadResult;
  expected: UploadExpectation;
  ok: boolean;
}) {
  const { t } = useTranslation('updates');
  const Icon = ok ? CheckCircle2 : XCircle;
  return (
    <div
      className={
        ok
          ? 'rounded-md border border-border bg-muted/30 p-3 text-sm'
          : 'rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm'
      }
      role="status"
    >
      <p className={ok ? 'flex items-center gap-2 font-medium text-foreground' : 'flex items-center gap-2 font-medium text-destructive'}>
        <Icon size={16} aria-hidden />
        {ok ? t('verifiedTitle') : t('verifyFailedTitle')}
      </p>
      <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <dt>{t('verifySize')}</dt>
        <dd>
          {t('verifyDeclaredServer', {
            declared: expected.sizeBytes,
            server: result.sizeBytes,
          })}
        </dd>
        <dt>{t('verifyDigest')}</dt>
        <dd className="font-mono">
          {t('verifyDeclaredServer', {
            declared: shortDigest(expected.digest),
            server: shortDigest(result.digest),
          })}
        </dd>
        <dt>{t('verifyState')}</dt>
        <dd>{artifactStateLabel(t, result.state)}</dd>
      </dl>
      {!ok && <p className="mt-2 text-xs text-destructive">{t('verifyFailedBody')}</p>}
    </div>
  );
}
