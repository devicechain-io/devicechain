// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Registers an X.509 certificate credential for a device: paste (or choose) the device's
// certificate in PEM, optionally followed by the intermediates that link it to the anchor, and
// pick the trust anchor its chain must reach.
//
// The credential's id is the certificate's SHA-256 pin and is COMPUTED BY THE SERVER; the form
// has no field for it and shows the value the server returns. The chain is verified when the
// credential is created, so a certificate that could not authenticate is refused here, with the
// reason, rather than stored. A device holds at most three certificate credentials.

import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Plus } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FormField } from '@/components/ui/form-field';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';
import { createDeviceCredential, MAX_X509_CREDENTIALS_PER_DEVICE } from '@/lib/api/credentials';
import { listTrustAnchors } from '@/lib/api/trust-anchors';
import { PemInput } from '@/routes/trust-anchors/PemInput';
import { checkPem, isAnchorExpired } from '@/routes/trust-anchors/anchorRules';
import { x509ErrorText } from '@/routes/trust-anchors/anchorErrors';

/** What the server computed for the credential just created. */
export interface PinnedCertificate {
  fingerprintSha256: string;
  subjectCn: string;
  notAfter: string;
}

export function X509CredentialForm({
  deviceToken,
  heldCount,
  onAdded,
}: {
  deviceToken: string;
  /** How many certificate credentials the device already holds. */
  heldCount: number;
  onAdded: () => void;
}) {
  const { t } = useTranslation('trustAnchors');
  const { toast } = useToast();
  const [pem, setPem] = useState('');
  const [anchorToken, setAnchorToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [pinned, setPinned] = useState<PinnedCertificate | null>(null);

  const { data: anchors, loading } = useQuery(() => listTrustAnchors({ pageNumber: 1, pageSize: 100 }), []);
  // Only an anchor that can vouch for a chain right now is offered: the server refuses the rest.
  const usable = (anchors?.results ?? []).filter((a) => a.enabled && !isAnchorExpired(a.notAfter));
  const options: ComboboxOption[] = usable.map((a) => ({
    value: a.token,
    label: a.name || a.token,
    description: a.name ? a.token : undefined,
  }));

  const atCap = heldCount >= MAX_X509_CREDENTIALS_PER_DEVICE;
  const pemProblem = checkPem(pem, { exactlyOne: false });

  const add = async () => {
    setFormError(null);
    setPinned(null);
    setBusy(true);
    try {
      const created = await createDeviceCredential({
        token: crypto.randomUUID(),
        deviceToken,
        credentialType: 'X509_CERTIFICATE',
        // Computed by the server from the certificate; sending one is refused.
        credentialId: '',
        certificatePem: pem.trim(),
        trustAnchorToken: anchorToken,
        enabled: true,
      });
      toast(t('credentialAdded'));
      setPinned({
        fingerprintSha256: created.certificate?.fingerprintSha256 ?? created.credentialId,
        subjectCn: created.certificate?.subjectCn ?? '',
        notAfter: created.certificate?.notAfter ?? created.expiresAt ?? '',
      });
      setPem('');
      onAdded();
    } catch (err) {
      setFormError(x509ErrorText(err, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {pinned && (
        <div role="status" className="space-y-1 rounded-lg border border-border bg-card p-3 text-sm">
          <div className="font-medium text-foreground">{t('pinnedTitle')}</div>
          <p className="text-xs text-muted-foreground">{t('pinnedHint')}</p>
          <div className="break-all font-mono text-xs text-foreground" data-testid="pinned-fingerprint">
            {pinned.fingerprintSha256}
          </div>
          {pinned.subjectCn && (
            <div className="text-xs text-muted-foreground">{t('pinnedCn', { cn: pinned.subjectCn })}</div>
          )}
          {pinned.notAfter && (
            <div className="text-xs text-muted-foreground">
              {t('pinnedExpires', { when: formatTime(pinned.notAfter) })}
            </div>
          )}
        </div>
      )}
      {atCap && (
        <p role="status" className="rounded-md border border-warning/40 bg-warning/5 p-3 text-sm text-foreground">
          {t('credentialCapReached', { max: MAX_X509_CREDENTIALS_PER_DEVICE })}
        </p>
      )}
      <FormField label={t('credentialPemLabel')} htmlFor="x509-pem" description={t('credentialPemHelp')}>
        <PemInput
          id="x509-pem"
          label={t('credentialPemLabel')}
          value={pem}
          onChange={setPem}
          exactlyOne={false}
          placeholder={t('pemPlaceholder')}
          disabled={atCap}
        />
      </FormField>
      <FormField label={t('credentialAnchorLabel')} htmlFor="x509-anchor" description={t('credentialAnchorHelp')}>
        <Combobox
          id="x509-anchor"
          value={anchorToken}
          onChange={setAnchorToken}
          options={options}
          placeholder={t('credentialAnchorSelect')}
          emptyMessage={t('credentialAnchorEmpty')}
          disabled={atCap || loading}
          allowClear={false}
        />
        {!loading && options.length === 0 && (
          <p className="mt-1 text-xs text-muted-foreground">
            <Link className="text-primary underline-offset-2 hover:underline" to="/trust-anchors">
              {t('goToAnchors')}
            </Link>
          </p>
        )}
      </FormField>
      <Button onClick={add} loading={busy} disabled={busy || atCap || !anchorToken || pem.trim() === '' || pemProblem !== null}>
        <Plus size={14} /> {t('addCertificateCredential')}
      </Button>
    </div>
  );
}
