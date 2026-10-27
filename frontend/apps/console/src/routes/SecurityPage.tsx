// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type FormEvent, type ReactNode } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { isExpired } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { ErrorState } from '@/components/ui/error-state';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { LoadingState } from '@/components/ui/loading-state';
import { useToast } from '@/components/ui/toast';
import { Logo } from '@/components/brand/Logo';
import { LocaleSwitcher } from '@/components/LocaleSwitcher';
import { QrCode } from '@/components/mfa/QrCode';
import { RecoveryCodesPanel } from '@/components/mfa/RecoveryCodesPanel';
import { mfaErrorKey } from '@/i18n/mfaError';
import {
  beginTotpEnrollment,
  confirmTotpEnrollment,
  disableTotp,
  getMfaStatus,
  regenerateRecoveryCodes,
  type TotpEnrollment,
} from '@/lib/api/mfa';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';

// The signed-in person's own two-factor authentication: set it up, see where it stands, get a
// fresh set of recovery codes, turn it off.
//
// It is a standalone page rather than a screen inside the tenant shell, because the person who
// most needs it has no tenant session: a member whose tenant requires a second factor and who
// has none is refused entry (MFA_ENROLLMENT_REQUIRED) and arrives here with only the identity
// session sign-in gave them. Everything on it therefore runs on the IDENTITY token, which the
// server takes as an argument, and re-checks the password for every change because identity
// tokens travel.
//
// 🔴 SECRETS. The enrolment secret and the recovery codes are shown once and held in component
// state only. They are not read through useQuery (which re-reads and caches), stored, put in
// the address or logged; each is dropped when the step that shows it ends, and with it the DOM
// that displayed it. The password typed for a step is cleared the moment the step finishes.
//
// Turning it ON rotates the account's sessions: the identity token that enrolled stops working
// and the server hands back a replacement, which is swapped in at once (adoptIdentity), so the
// person carries on without signing in again. Turning it OFF ends every session, this one
// included, and the page then leaves for the sign-in screen.

type Flow = 'idle' | 'enroll' | 'regenerate' | 'disable';

export default function SecurityPage() {
  const { t } = useTranslation('security');
  const { isIdentityAuthenticated, isAuthenticated, identityToken, logout } = useAuth();
  const navigate = useNavigate();

  if (!isIdentityAuthenticated || identityToken === null) {
    // A tenant session without a live identity token (the identity token lasts 15 minutes and
    // cannot be renewed from the tenant session): ask for a fresh sign-in, rather than
    // bouncing a signed-in person to a login screen that redirects straight back.
    if (isAuthenticated) {
      return (
        <Shell>
          <p className="text-sm text-foreground">{t('signInAgain')}</p>
          <div className="flex gap-2">
            <Button onClick={() => void logout()}>{t('signInAgainButton')}</Button>
            <Button variant="ghost" onClick={() => navigate('/')}>
              {t('backToConsole')}
            </Button>
          </div>
        </Shell>
      );
    }
    return <Navigate to="/login" replace />;
  }
  return <SecurityContent identityToken={identityToken} />;
}

function Shell({ children }: { children: ReactNode }) {
  const { t } = useTranslation('security');
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4 py-8">
      <div className="w-full max-w-lg">
        <div className="mb-6 flex flex-col items-center text-center">
          <Logo deviceColor="currentColor" className="mb-4 h-14 w-auto" />
          <h1 className="text-lg font-semibold text-foreground">{t('title')}</h1>
        </div>
        <div className="space-y-4 rounded-lg border border-border bg-card p-6" data-shot="security-page">
          {children}
        </div>
        <div className="mt-6 flex justify-center">
          <LocaleSwitcher />
        </div>
      </div>
    </div>
  );
}

function SecurityContent({ identityToken }: { identityToken: string }) {
  const { t } = useTranslation('security');
  const { isAuthenticated, selectTenant, forgetSessions } = useAuth();
  const { toast } = useToast();
  const navigate = useNavigate();
  const location = useLocation();
  // Set by the sign-in screen when it sent the person here to meet a tenant's requirement.
  const enrollForTenant =
    (location.state as { enrollForTenant?: string } | null)?.enrollForTenant ?? null;

  // (The setters are named here, above the render, because string literals inside JSX
  // handlers trip the no-literal-string rule: the flows are identifiers, not copy.)
  const [flow, setFlow] = useState<Flow>(enrollForTenant ? 'enroll' : 'idle');
  const toIdle = () => setFlow('idle');
  const toEnroll = () => setFlow('enroll');
  const toRegenerate = () => setFlow('regenerate');
  const toDisable = () => setFlow('disable');
  // The recovery codes being shown, once. Cleared by acknowledging.
  const [codes, setCodes] = useState<string[] | null>(null);
  const [version, setVersion] = useState(0);
  const [error, setError] = useState<string | null>(null);

  // `current` rather than `data`: the buttons below act on whether the account is enrolled, so
  // they must answer to THIS token's read and not a previous one.
  const { current: status, loading, error: loadError } = useQuery(
    () => getMfaStatus(identityToken),
    [identityToken, version],
  );

  const leave = () => navigate(isAuthenticated ? '/' : '/login', { replace: true });

  const codesDone = async () => {
    setCodes(null);
    setError(null);
    if (enrollForTenant) {
      // Back to what they were doing: enter the tenant that asked for the factor.
      try {
        await selectTenant(identityToken, enrollForTenant);
        navigate('/', { replace: true });
      } catch (err) {
        setError(t(mfaErrorKey(err, 'login:enterTenantFailed')));
        setVersion((v) => v + 1);
      }
      return;
    }
    setVersion((v) => v + 1);
  };

  // Turning the factor off ended every session the account had, this one included.
  const disabled = () => {
    forgetSessions();
    toast(t('disabledToast'));
    navigate('/login', { replace: true });
  };

  let body: ReactNode;
  if (codes) {
    body = <RecoveryCodesPanel codes={codes} onDone={() => void codesDone()} />;
  } else if (flow === 'enroll') {
    body = (
      <EnrollFlow
        identityToken={identityToken}
        onCancel={() => toIdle()}
        onEnrolled={(c) => {
          toIdle();
          setCodes(c);
        }}
      />
    );
  } else if (flow === 'regenerate') {
    body = (
      <PasswordCodeForm
        title={t('regenerateTitle')}
        description={t('regenerateDescription')}
        submitLabel={t('regenerateSubmit')}
        identityToken={identityToken}
        onCancel={() => toIdle()}
        run={async (password, code) => {
          const fresh = await regenerateRecoveryCodes(identityToken, password, code);
          toIdle();
          setCodes(fresh);
        }}
      />
    );
  } else if (flow === 'disable') {
    body = (
      <PasswordCodeForm
        title={t('disableTitle')}
        description={t('disableDescription')}
        warning={t('disableWarning')}
        submitLabel={t('disableSubmit')}
        destructive
        identityToken={identityToken}
        onCancel={() => toIdle()}
        run={async (password, code) => {
          await disableTotp(identityToken, password, code);
          disabled();
        }}
      />
    );
  } else if (loading && !status) {
    body = <LoadingState description={t('loading')} />;
  } else if (loadError || !status) {
    body = <ErrorState description={loadError ?? t('loadFailed')} />;
  } else {
    body = (
      <div className="space-y-4">
        {enrollForTenant && !status.totpEnabled && (
          <p className="text-sm text-foreground">{t('tenantRequires', { tenant: enrollForTenant })}</p>
        )}
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-foreground">{t('statusLabel')}</span>
          <Badge variant={status.totpEnabled ? 'success' : 'outline'} data-shot="security-status">
            {status.totpEnabled ? t('statusOn') : t('statusOff')}
          </Badge>
        </div>
        {status.totpEnabled ? (
          <>
            <dl className="space-y-1 text-sm">
              {status.enrolledAt && (
                <div className="flex gap-2">
                  <dt className="text-muted-foreground">{t('enrolledAt')}</dt>
                  <dd>{formatTime(status.enrolledAt)}</dd>
                </div>
              )}
              <div className="flex gap-2">
                <dt className="text-muted-foreground">{t('codesRemaining')}</dt>
                <dd>{status.recoveryCodesRemaining}</dd>
              </div>
            </dl>
            {status.recoveryCodesRemaining <= 2 && (
              <p className="text-sm text-destructive">{t('codesLow')}</p>
            )}
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => toRegenerate()}>
                {t('regenerate')}
              </Button>
              <Button variant="outline" onClick={() => toDisable()}>
                {t('disable')}
              </Button>
            </div>
          </>
        ) : (
          <>
            <p className="text-sm text-muted-foreground">{t('enrollIntro')}</p>
            <Button onClick={() => toEnroll()} data-shot="security-enroll">
              {t('enrollStart')}
            </Button>
          </>
        )}
      </div>
    );
  }

  const idle = !codes && flow === 'idle';
  return (
    <Shell>
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      {body}
      {idle && (
        <div className="border-t border-border pt-4">
          <Button variant="ghost" onClick={leave}>
            {isAuthenticated ? t('backToConsole') : t('backToSignIn')}
          </Button>
        </div>
      )}
    </Shell>
  );
}

// ── Enrolment ───────────────────────────────────────────────────────────────

// Two steps: the password (which the server re-checks) starts the enrolment and returns the
// secret; the code from the authenticator app proves the app holds it. The password is kept
// from the first step for the second (confirming re-checks it) and cleared when the flow ends.
function EnrollFlow({
  identityToken,
  onCancel,
  onEnrolled,
}: {
  identityToken: string;
  onCancel: () => void;
  onEnrolled: (codes: string[]) => void;
}) {
  const { t } = useTranslation('security');
  const { adoptIdentity } = useAuth();
  const [password, setPassword] = useState('');
  const [enrollment, setEnrollment] = useState<TotpEnrollment | null>(null);
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const begin = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (isExpired(identityToken, 0)) {
      setError(t('sessionEnded'));
      return;
    }
    setBusy(true);
    try {
      setEnrollment(await beginTotpEnrollment(identityToken, password));
    } catch (err) {
      setError(t(mfaErrorKey(err, 'security:passwordRejected')));
    } finally {
      setBusy(false);
    }
  };

  const confirm = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const result = await confirmTotpEnrollment(identityToken, password, code);
      // The sign-in that enrolled has just ended; this is its replacement.
      await adoptIdentity(result.auth);
      setPassword('');
      setEnrollment(null);
      onEnrolled(result.codes);
    } catch (err) {
      setCode('');
      setError(t(mfaErrorKey(err, 'security:enrollCodeRejected')));
      setBusy(false);
    }
  };

  // Start over: the pending secret is abandoned (the server expires it) and a new one is
  // issued by the next begin.
  const startOver = () => {
    setEnrollment(null);
    setCode('');
    setError(null);
  };

  if (!enrollment) {
    return (
      <form onSubmit={begin} className="space-y-4" data-shot="security-enroll-password">
        <h2 className="text-base font-semibold text-foreground">{t('enrollTitle')}</h2>
        <p className="text-sm text-muted-foreground">{t('enrollPasswordIntro')}</p>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <FormField label={t('passwordLabel')} htmlFor="enroll-password">
          <Input
            id="enroll-password"
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </FormField>
        <div className="flex gap-2">
          <Button type="submit" loading={busy} disabled={busy || password === ''}>
            {t('enrollContinue')}
          </Button>
          <Button
            type="button"
            variant="ghost"
            disabled={busy}
            onClick={() => {
              setPassword('');
              onCancel();
            }}
          >
            {t('cancel')}
          </Button>
        </div>
      </form>
    );
  }

  return (
    <form onSubmit={confirm} className="space-y-4" data-shot="security-enroll-scan">
      <h2 className="text-base font-semibold text-foreground">{t('enrollScanTitle')}</h2>
      <p className="text-sm text-muted-foreground">{t('enrollScanIntro')}</p>
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <div className="flex justify-center" data-shot-pii>
        <QrCode value={enrollment.otpauthUri} label={t('qrLabel')} />
      </div>
      <FormField label={t('secretLabel')} description={t('secretHint')}>
        <code className="block break-all rounded-md border border-border bg-muted/40 px-3 py-2 font-mono text-sm" data-shot-pii>
          {enrollment.secret}
        </code>
      </FormField>
      <FormField label={t('enrollCodeLabel')} htmlFor="enroll-code" description={t('enrollCodeHint')}>
        <Input
          id="enroll-code"
          type="text"
          inputMode="numeric"
          autoComplete="one-time-code"
          autoFocus
          value={code}
          onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
          required
        />
      </FormField>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" loading={busy} disabled={busy || code.length !== 6}>
          {t('enrollConfirm')}
        </Button>
        <Button type="button" variant="outline" disabled={busy} onClick={startOver}>
          {t('startOver')}
        </Button>
        <Button
          type="button"
          variant="ghost"
          disabled={busy}
          onClick={() => {
            setPassword('');
            setEnrollment(null);
            onCancel();
          }}
        >
          {t('cancel')}
        </Button>
      </div>
    </form>
  );
}

// ── Regenerate / disable ────────────────────────────────────────────────────

// Both need the password AND a code (an authenticator code or an unused recovery code), so a
// stolen identity token alone changes nothing.
function PasswordCodeForm({
  title,
  description,
  warning,
  submitLabel,
  destructive,
  identityToken,
  onCancel,
  run,
}: {
  title: string;
  description: string;
  warning?: string;
  submitLabel: string;
  destructive?: boolean;
  identityToken: string;
  onCancel: () => void;
  run: (password: string, code: string) => Promise<void>;
}) {
  const { t } = useTranslation('security');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (isExpired(identityToken, 0)) {
      setError(t('sessionEnded'));
      return;
    }
    setBusy(true);
    try {
      await run(password, code);
      // On success the parent has moved on and this form is going away.
    } catch (err) {
      setCode('');
      setError(t(mfaErrorKey(err, 'security:codeOrPasswordRejected')));
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4" data-shot="security-password-code">
      <h2 className="text-base font-semibold text-foreground">{title}</h2>
      <p className="text-sm text-muted-foreground">{description}</p>
      {warning && (
        <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {warning}
        </p>
      )}
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <FormField label={t('passwordLabel')} htmlFor="mfa-password">
        <Input
          id="mfa-password"
          type="password"
          autoComplete="current-password"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
      </FormField>
      <FormField label={t('anyCodeLabel')} htmlFor="mfa-any-code" description={t('anyCodeHint')}>
        <Input
          id="mfa-any-code"
          type="text"
          autoComplete="one-time-code"
          spellCheck={false}
          value={code}
          onChange={(e) => setCode(e.target.value.toUpperCase().replace(/[^A-Z0-9-]/g, '').slice(0, 19))}
          required
        />
      </FormField>
      <div className="flex gap-2">
        <Button
          type="submit"
          variant={destructive ? 'destructive' : 'default'}
          loading={busy}
          disabled={busy || password === '' || code === ''}
        >
          {submitLabel}
        </Button>
        <Button
          type="button"
          variant="ghost"
          disabled={busy}
          onClick={() => {
            setPassword('');
            setCode('');
            onCancel();
          }}
        >
          {t('cancel')}
        </Button>
      </div>
    </form>
  );
}
