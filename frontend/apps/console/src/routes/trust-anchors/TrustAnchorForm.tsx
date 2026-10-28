// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The trust-anchor create form: a token, a name and one CA certificate in PEM (pasted or
// uploaded). The certificate cannot be changed afterwards (another CA is another anchor), so
// there is no edit mode. The server validates the PEM and its refusal is shown as it said it.

import '@/routes/trust-anchors/catalog';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { createTrustAnchor, getTrustAnchor } from '@/lib/api/trust-anchors';
import { PemInput } from './PemInput';
import { checkPem } from './anchorRules';
import { x509ErrorText } from './anchorErrors';

export function TrustAnchorForm({ onCreated }: { onCreated: (token: string) => void }) {
  const { t } = useTranslation(['trustAnchors', 'common']);
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [pem, setPem] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const pemProblem = checkPem(pem, { exactlyOne: true });

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const created = await createTrustAnchor({
        token: token.trim(),
        name: name.trim() || null,
        certificatePem: pem.trim(),
      });
      onCreated(created.token);
    } catch (err) {
      // The server's validation (not a CA, expired, name constraints, a key it does not allow,
      // a duplicate, the cap of 32) is what the operator needs to read.
      setFormError(x509ErrorText(err, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField label={t('common:colToken')} htmlFor="ta-token">
        <TokenField
          id="ta-token"
          entityType="trust-anchor"
          value={token}
          onChange={setToken}
          seed={name}
          placeholder={t('trustAnchors:tokenPlaceholder')}
          checkAvailability={(tok) => getTrustAnchor(tok).then((a) => a === null)}
        />
      </FormField>
      <FormField label={t('common:colName')} htmlFor="ta-name">
        <Input id="ta-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label={t('trustAnchors:pemLabel')} htmlFor="ta-pem" description={t('trustAnchors:pemHelp')}>
        <PemInput
          id="ta-pem"
          label={t('trustAnchors:pemLabel')}
          value={pem}
          onChange={setPem}
          exactlyOne
          placeholder={t('trustAnchors:pemPlaceholder')}
        />
      </FormField>
      <Button onClick={submit} loading={busy} disabled={busy || !token.trim() || pemProblem !== null}>
        {t('trustAnchors:createAction')}
      </Button>
    </div>
  );
}
