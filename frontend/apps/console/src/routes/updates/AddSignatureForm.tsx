// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Attach a signature someone produced OFFLINE to an artifact. The platform never signs and
// never holds a private key; the operator signs the artifact's digest with their own tooling
// (see the signing guide) and pastes the result here.
//
// The operator names the signer by its KEY ID, the identity printed by the tooling, but the
// mutation takes the signer's TOKEN. So the form resolves the key ID against the tenant's
// signer registry and says who it resolved to BEFORE anything is sent. It fails closed on
// every way that resolution can be unsure:
//   - the registry could not be read       -> no submit, the read error is shown;
//   - no registered signer has that key ID -> no submit, and the form says so;
//   - the signer is REVOKED                -> no submit (its signatures no longer count).
// The service remains the authority: whatever it refuses (SIGNATURE_INVALID, SIGNER_REVOKED,
// ...) is shown exactly as it said it, never paraphrased into something friendlier.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { PenLine } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { addArtifactSignature, listArtifactSigners, type Artifact } from '@/lib/api/update-management';
import { SignerStateBadge } from './badges';
import '@/i18n/lazy/updates';

// Standard base64 (RFC 4648 section 4), padded. The service decodes the same alphabet.
const BASE64 = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;

export function AddSignatureForm({
  artifactToken,
  reloadKey,
  onAdded,
}: {
  artifactToken: string;
  /** The page's reload counter: the registry is re-read with the page, so a signer revoked
   * since the form mounted is not still offered as ACTIVE. */
  reloadKey: number;
  onAdded: (artifact: Artifact) => void;
}) {
  const { t } = useTranslation('updates');
  const { toast } = useToast();
  const signers = useQuery(() => listArtifactSigners({ pageNumber: 1, pageSize: 1000 }), [reloadKey]);
  const [keyId, setKeyId] = useState('');
  const [signature, setSignature] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const wanted = keyId.trim().toLowerCase();
  const sig = signature.replace(/\s+/g, '');
  const sigValid = sig.length > 0 && BASE64.test(sig);
  const list = signers.data?.results ?? null;
  const match = wanted && list ? list.find((s) => s.keyId.toLowerCase() === wanted) : undefined;

  // Why the form cannot be submitted, as the sentence shown under the key ID field.
  let keyProblem: string | null = null;
  if (signers.error && !list) keyProblem = t('signersUnreadable', { error: signers.error });
  else if (wanted && list && !match) {
    const truncated = (signers.data?.pagination.totalRecords ?? 0) > list.length;
    keyProblem = truncated ? t('signerNotFoundTruncated') : t('signerNotFound');
  } else if (match?.state === 'REVOKED') keyProblem = t('signerIsRevoked');

  const canSubmit = !!match && match.state === 'ACTIVE' && sigValid && !busy;

  const submit = async () => {
    if (!canSubmit || !match) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await addArtifactSignature({
        artifactToken,
        signerToken: match.token,
        signature: sig,
      });
      setSignature('');
      setKeyId('');
      toast(t('signatureAdded', { trust: t(updated.trustStatus === 'SIGNED' ? 'trustSigned' : 'trustUnverified') }));
      onAdded(updated);
    } catch (err) {
      // Verbatim: the refusal code and wording are what a signing runbook tells the operator
      // to look for.
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
      data-testid="add-signature-form"
    >
      <FormField label={t('fieldSignerKeyId')} htmlFor="signature-key-id" description={t('fieldSignerKeyIdHelp')}>
        <Input
          id="signature-key-id"
          className="font-mono"
          value={keyId}
          onChange={(e) => setKeyId(e.target.value)}
          autoComplete="off"
          spellCheck={false}
        />
        {match && (
          <p className="mt-1 flex items-center gap-2 text-sm text-muted-foreground">
            {t('resolvedSigner', { name: match.name })} <SignerStateBadge state={match.state} />
          </p>
        )}
        {keyProblem && <p className="mt-1 text-sm text-destructive">{keyProblem}</p>}
      </FormField>
      <FormField label={t('fieldSignature')} htmlFor="signature-value" description={t('fieldSignatureHelp')}>
        <Textarea
          id="signature-value"
          className="font-mono"
          rows={4}
          value={signature}
          onChange={(e) => setSignature(e.target.value)}
          spellCheck={false}
        />
        {sig.length > 0 && !sigValid && <p className="mt-1 text-sm text-destructive">{t('signatureNotBase64')}</p>}
      </FormField>
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <HintText size="md">{t('signatureVerifiedByService')}</HintText>
      <Button type="submit" loading={busy} disabled={!canSubmit}>
        <PenLine size={14} /> {t('addSignature')}
      </Button>
    </form>
  );
}
