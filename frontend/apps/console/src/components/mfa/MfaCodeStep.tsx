// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { mfaErrorKey } from '@/i18n/mfaError';
import { serverErrorCode } from '@/i18n/signInError';
import { MFA_CODE, challengeNoticeDelayMs, type MfaChallenge } from '@/lib/mfaChallenge';
import type { IdentityAuth } from '@/lib/api/user-management';

// The second step of a sign-in that login held for a second factor: ask for the code (an
// authenticator's six digits, or a recovery code) and finish with verifyMfa. Shared by every
// screen that signs a password in (the sign-in page, an invitation), because each needs the
// same answers to the same failures:
//
//   - every refusal of the code is ONE message, because the server does not say whether it was
//     wrong, replayed or locked; the per-source rate limit and an outage read as themselves;
//   - the SERVER decides when a challenge is over (MFA_CHALLENGE_EXPIRED). The browser's clock
//     is never compared with `expiresAt` to refuse a submit, since a clock a few minutes off
//     would lock out a person the server would accept. The notice that a challenge has
//     probably lapsed is a timer counted from when it was received;
//   - the handle lives in the parent's state only (never an address, never storage).
export function MfaCodeStep({
  challenge,
  onVerified,
  onRestart,
}: {
  challenge: MfaChallenge;
  /** Called with the finished sign-in. It reports its own failures: this keeps the form busy until it settles. */
  onVerified: (auth: IdentityAuth) => Promise<void>;
  /** The challenge is over (or the person went back): start from the password. `message` is shown there. */
  onRestart: (message: string | null) => void;
}) {
  const { t } = useTranslation('login');
  const { verifyMfa } = useAuth();
  const [code, setCode] = useState('');
  const [useRecovery, setUseRecovery] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // Counted from receipt, not from a comparison with the server's timestamp (see above).
  const expiresAt = challenge.expiresAt;
  useEffect(() => {
    const timer = setTimeout(() => onRestart(t('mfaChallengeExpired')), challengeNoticeDelayMs(expiresAt));
    return () => clearTimeout(timer);
    // The challenge is the identity of this step: a new one restarts the clock.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [challenge.challenge]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    let auth: IdentityAuth;
    try {
      auth = await verifyMfa(challenge.challenge, code);
    } catch (err) {
      setCode('');
      if (serverErrorCode(err) === MFA_CODE.challengeExpired) {
        onRestart(t('mfaChallengeExpired'));
        return;
      }
      setError(t(mfaErrorKey(err, 'login:mfaInvalidCode')));
      setBusy(false);
      return;
    }
    await onVerified(auth);
    if (alive.current) setBusy(false);
  };

  const ready = useRecovery ? code.length >= 8 : code.length === 6;
  const canRecover = challenge.methods.length === 0 || challenge.methods.includes('RECOVERY_CODE');

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <FormField
        label={useRecovery ? t('mfaRecoveryLabel') : t('mfaCodeLabel')}
        htmlFor="mfa-code"
        description={useRecovery ? t('mfaRecoveryHint') : t('mfaCodeHint')}
      >
        <Input
          id="mfa-code"
          data-shot="login-mfa-code"
          type="text"
          inputMode={useRecovery ? 'text' : 'numeric'}
          autoComplete="one-time-code"
          spellCheck={false}
          autoFocus
          value={code}
          onChange={(e) =>
            setCode(
              useRecovery
                ? e.target.value.toUpperCase().replace(/[^A-Z0-9-]/g, '').slice(0, 19)
                : e.target.value.replace(/\D/g, '').slice(0, 6),
            )
          }
          required
        />
      </FormField>
      <Button type="submit" className="w-full" loading={busy} disabled={busy || !ready}>
        {t('mfaVerify')}
      </Button>
      {canRecover && (
        <Button
          type="button"
          variant="outline"
          className="w-full"
          disabled={busy}
          onClick={() => {
            setUseRecovery(!useRecovery);
            setCode('');
            setError(null);
          }}
        >
          {useRecovery ? t('mfaUseAuthenticator') : t('mfaUseRecovery')}
        </Button>
      )}
      <Button type="button" variant="ghost" className="w-full" disabled={busy} onClick={() => onRestart(null)}>
        {t('common:back')}
      </Button>
    </form>
  );
}
