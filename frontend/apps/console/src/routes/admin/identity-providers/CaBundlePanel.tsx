// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A connection's private CA bundle: extra trusted certificate authorities for THIS
// provider's TLS certificate, for an on-premises provider whose certificate a private CA
// signed. They are trusted in addition to the system's roots, for this connection only.
//
// 🔴 CERTIFICATES ONLY, AND NEVER A KEY. The input refuses text that looks like a private
// key before anything is sent, and the server refuses it too. The bundle is supplied here,
// as text or a file the operator chooses: there is no address to fetch one from, because a
// fetched trust root is a trust root chosen by whoever answers the fetch.
//
// The server checks a new bundle by fetching the provider's discovery document under it
// before saving, so a bundle that does not verify the provider is refused with the reason
// and the stored one is untouched.

import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Upload } from 'lucide-react';
import { setIdpCaBundle, type IdpCaCertificate, type IdpConnection } from '@/lib/api/identity-providers';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FilePicker } from '@/components/ui/file-picker';
import { SectionPanel } from '@/components/ui/section-panel';
import { Textarea } from '@/components/ui/textarea';
import { useToast } from '@/components/ui/toast';
import { errMessage } from '@/routes/common';
import { CA_BUNDLE_MAX_BYTES, checkCaBundle } from './idpForm';
import '@/i18n/lazy/identityProviders';

// The textarea and the file chooser, one control: choosing a file puts its text in the box
// so the operator sees exactly what will be sent.
export function CaBundleInput({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (text: string) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation('identityProviders');
  const [fileError, setFileError] = useState<string | null>(null);
  const id = useId();
  const problem = checkCaBundle(value);

  const pick = async (file: File) => {
    setFileError(null);
    if (file.size > CA_BUNDLE_MAX_BYTES) {
      setFileError(t('caBundleTooLargeError'));
      return;
    }
    try {
      onChange(await file.text());
    } catch {
      setFileError(t('caBundleUnreadableError'));
    }
  };

  return (
    <div className="space-y-2">
      <Textarea
        id={id}
        aria-label={t('caBundleLabel')}
        value={value}
        rows={6}
        spellCheck={false}
        disabled={disabled}
        placeholder={t('caBundlePlaceholder')}
        className="text-xs"
        onChange={(e) => onChange(e.target.value)}
      />
      <div className="flex items-center gap-2">
        <FilePicker onPick={(f) => void pick(f)} accept=".pem,.crt,.cer,.ca-bundle,text/plain" disabled={disabled}>
          <Upload size={14} /> {t('caBundleChooseFile')}
        </FilePicker>
      </div>
      {(fileError || problem) && (
        <p role="alert" className="text-sm text-destructive">
          {fileError ?? t(problem as string)}
        </p>
      )}
    </div>
  );
}

function CertificateRow({ cert }: { cert: IdpCaCertificate }) {
  const { t } = useTranslation('identityProviders');
  const expired = new Date(cert.notAfter).getTime() < Date.now();
  return (
    <li className="space-y-1 rounded-md border p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium break-all">{cert.subject}</span>
        {expired && <Badge variant="destructive">{t('caCertExpiredBadge')}</Badge>}
        {!cert.isCa && <Badge variant="outline">{t('caCertNotCaBadge')}</Badge>}
      </div>
      <div className="text-xs text-muted-foreground">
        {t('caCertValidUntil', { date: cert.notAfter.slice(0, 10) })}
      </div>
      <div className="break-all font-mono text-xs text-muted-foreground">
        {t('caCertFingerprint', { fingerprint: cert.fingerprintSha256 })}
      </div>
      {!cert.isCa && <p className="text-xs text-muted-foreground">{t('caCertNotCaHint')}</p>}
    </li>
  );
}

export function CaBundlePanel({
  connection: c,
  reading,
  onChanged,
}: {
  connection: IdpConnection;
  reading: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [draft, setDraft] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const run = async (text: string, done: string) => {
    setError(null);
    setBusy(true);
    try {
      await setIdpCaBundle(c.token, text);
      setDraft('');
      toast(done);
      onChanged();
    } catch (err) {
      // e.g. the provider's certificate does not chain to this bundle, or a private key
      // was pasted. Shown as the server said it.
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const save = () => {
    const problem = checkCaBundle(draft);
    if (problem) return setError(t(problem));
    if (draft.trim() === '') return setError(t('caBundleRequiredError'));
    void run(draft.trim(), t('caBundleSavedToast'));
  };

  const clear = async () => {
    const ok = await confirm({
      title: t('caBundleClearTitle'),
      description: t('caBundleClearDescription', { name: c.name || c.token }),
      confirmLabel: t('caBundleClearButton'),
    });
    if (ok) void run('', t('caBundleClearedToast'));
  };

  return (
    <SectionPanel title={t('caBundleTitle')} description={t('caBundleDescription')}>
      <div className="space-y-4">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        {c.caCertificates.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('caBundleNone')}</p>
        ) : (
          <ul className="space-y-2" aria-label={t('caBundleCertsLabel')}>
            {c.caCertificates.map((cert) => (
              <CertificateRow key={cert.fingerprintSha256} cert={cert} />
            ))}
          </ul>
        )}
        <div className="space-y-2">
          <p className="text-sm font-medium">
            {c.caCertificates.length === 0 ? t('caBundleSetHeading') : t('caBundleReplaceHeading')}
          </p>
          <CaBundleInput value={draft} onChange={setDraft} disabled={busy || reading} />
          <p className="text-xs text-muted-foreground">{t('caBundleVerifyNote')}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button onClick={save} loading={busy} disabled={busy || reading || draft.trim() === ''}>
            {t('caBundleSaveButton')}
          </Button>
          {c.caCertificates.length > 0 && (
            <Button variant="outline" onClick={() => void clear()} disabled={busy || reading}>
              {t('caBundleClearButton')}
            </Button>
          )}
        </div>
      </div>
    </SectionPanel>
  );
}
