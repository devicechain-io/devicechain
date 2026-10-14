// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// REGISTER A SIGNER: paste a PUBLIC key, name it, and the platform derives the rest.
//
// 🔴 A PRIVATE KEY MUST NEVER LEAVE THIS FORM, and must never be shown back. The
// server refuses one too, but a paste that reached the wire has already crossed a
// boundary the operator cannot take back, so the check is made here first. When it fires:
//   • nothing is sent;
//   • the field is CLEARED, so the secret is not left sitting in the DOM;
//   • the error is a fixed sentence — it never quotes, truncates or measures the pasted
//     text, and the text is never logged or toasted.
// A server refusal is a different case: the server does not echo keys, so its message is
// shown verbatim and the (public) key stays put for correction.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { registerArtifactSigner, type ArtifactSigner } from '@/lib/api/update-management';
import { errMessage } from '@/routes/common';

/** The published signing guide: how to generate a key and compute its key ID. */
export const SIGNING_GUIDE_URL = 'https://docs.devicechain.io/guides/signing-updates';

// Any PEM label that names a private key (PRIVATE KEY, EC PRIVATE KEY, ENCRYPTED PRIVATE
// KEY, RSA PRIVATE KEY...). Tolerant of case and of odd spacing on purpose: a false
// positive costs the operator a re-paste, a false negative ships a secret.
const PRIVATE_KEY_PATTERN = /PRIVATE\s+KEY/i;

export function RegisterSignerForm({ onRegistered }: { onRegistered: (signer: ArtifactSigner) => void }) {
  const { t } = useTranslation('updateSigners');
  const [name, setName] = useState('');
  const [pem, setPem] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (PRIVATE_KEY_PATTERN.test(pem)) {
      setPem('');
      setError(t('privateKeyRefused'));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const signer = await registerArtifactSigner({ name: name.trim(), publicKeyPem: pem.trim() });
      onRegistered(signer);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <FormField label={t('fieldName')} htmlFor="signer-name" description={t('fieldNameHint')}>
        <Input id="signer-name" value={name} onChange={(e) => setName(e.target.value)} required />
      </FormField>
      <FormField label={t('fieldPublicKey')} htmlFor="signer-pem" description={t('fieldPublicKeyHint')}>
        <Textarea
          id="signer-pem"
          rows={8}
          value={pem}
          onChange={(e) => setPem(e.target.value)}
          placeholder={t('fieldPublicKeyPlaceholder')}
          autoComplete="off"
          spellCheck={false}
          required
        />
      </FormField>
      <p className="text-sm text-muted-foreground">
        <a href={SIGNING_GUIDE_URL} target="_blank" rel="noopener noreferrer" className="text-primary underline">
          {t('guideLink')}
        </a>
      </p>
      <Button type="submit" disabled={busy || !name.trim() || !pem.trim()}>
        {busy ? t('registering') : t('register')}
      </Button>
    </form>
  );
}
