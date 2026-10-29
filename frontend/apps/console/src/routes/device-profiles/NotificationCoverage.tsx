// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm → human last mile, told honestly. Notification policies route by SEVERITY ONLY: there
// is no per-rule routing and a policy cannot be limited to a device type. So the question this
// panel can answer for a rule that raises an alarm is not "will THIS rule notify anyone" but
// "does any enabled policy send alarms of this rule's severity through a working channel" — and,
// when none does, it offers the one fix that exists: create a tenant-wide policy for that
// severity. Creating it routes EVERY alarm of that severity, and the copy says so.
//
// Advisory, like the compiler check beside it: it never blocks saving the rule. A failed read (the
// caller may lack notification:read) says the check could not run rather than guessing either way,
// and an instance without the notification area shows nothing at all.

import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { AREA, useAreaAbsent } from '@/lib/capabilities';
import { errMessage } from '@/routes/common';
import {
  createSeverityPolicy,
  listNotificationChannels,
  listNotificationPolicies,
  severityCovered,
  type NotificationChannel,
} from '@/lib/api/notifications';
import '@/i18n/lazy/deviceProfiles';

type Check = { status: 'loading' } | { status: 'covered' } | { status: 'uncovered' } | { status: 'unknown' };

// An smtp channel needs recipients; a webhook's endpoint is its own destination.
const SMTP = 'smtp';

export function NotificationCoverage({ severity }: { severity: string }) {
  const { t } = useTranslation('deviceProfiles');
  const absent = useAreaAbsent(AREA.notificationManagement);
  const { claims } = useAuth();
  // Creating a policy needs notification:write; without it the check still reads, the action is hidden.
  const canWrite = hasAuthority(claims, 'notification:write');
  const [check, setCheck] = useState<Check>({ status: 'loading' });
  const [creating, setCreating] = useState(false);

  const recheck = useCallback(() => {
    let cancelled = false;
    setCheck({ status: 'loading' });
    listNotificationPolicies()
      .then((ps) => {
        if (!cancelled) setCheck({ status: severityCovered(ps, severity) ? 'covered' : 'uncovered' });
      })
      .catch(() => {
        if (!cancelled) setCheck({ status: 'unknown' });
      });
    return () => {
      cancelled = true;
    };
  }, [severity]);

  useEffect(() => {
    if (absent) return;
    setCreating(false);
    return recheck();
  }, [absent, recheck]);

  if (absent) return null;

  const sev = t(`alarms:sev${severity[0].toUpperCase()}${severity.slice(1)}`).toLowerCase();
  return (
    <div className="space-y-2 rounded-md border p-3" data-shot="rule-coverage">
      <span className="text-sm font-semibold">{t('coverageTitle')}</span>
      {check.status === 'loading' && <p className="text-xs text-muted-foreground">{t('coverageChecking')}</p>}
      {check.status === 'unknown' && <p className="text-xs text-muted-foreground">{t('coverageUnknown')}</p>}
      {check.status === 'covered' && (
        <p className="text-sm text-emerald-600 dark:text-emerald-500" data-testid="coverage-covered">
          {t('coverageCovered', { severity: sev })}
        </p>
      )}
      {check.status === 'uncovered' && (
        <>
          <p className="text-sm text-amber-600 dark:text-amber-500" data-testid="coverage-uncovered">
            {t('coverageUncovered', { severity: sev })}
          </p>
          {!canWrite ? null : creating ? (
            <CreatePolicy
              severity={severity}
              severityLabel={sev}
              onCancel={() => setCreating(false)}
              onCreated={() => {
                setCreating(false);
                recheck();
              }}
            />
          ) : (
            <Button size="sm" variant="outline" data-shot="rule-coverage-create" onClick={() => setCreating(true)}>
              {t('coverageCreateButton', { severity: sev })}
            </Button>
          )}
        </>
      )}
    </div>
  );
}

function CreatePolicy({
  severity,
  severityLabel,
  onCancel,
  onCreated,
}: {
  severity: string;
  severityLabel: string;
  onCancel: () => void;
  onCreated: () => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const [channels, setChannels] = useState<NotificationChannel[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [channelToken, setChannelToken] = useState('');
  const [recipients, setRecipients] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    listNotificationChannels()
      .then((cs) => {
        if (cancelled) return;
        setChannels(cs);
        if (cs.length === 1) setChannelToken(cs[0].token);
      })
      .catch((err) => {
        if (!cancelled) setLoadError(errMessage(err));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const channel = channels?.find((c) => c.token === channelToken);
  const list = recipients
    .split(',')
    .map((r) => r.trim())
    .filter(Boolean);
  const needsRecipients = channel?.channelType === SMTP;
  const ready = channel != null && (!needsRecipients || list.length > 0);

  const submit = async () => {
    if (!channel) return;
    setBusy(true);
    setError(null);
    try {
      await createSeverityPolicy({
        // Unique per click: a tenant may already hold a policy (disabled, or for another channel) by
        // the obvious name, and a collision would refuse the create for a reason the operator can't see.
        token: `notify-${severity}-${Date.now().toString(36)}`,
        name: t('coveragePolicyName', { severity: severityLabel }),
        severity,
        channelToken: channel.token,
        recipients: needsRecipients ? list : undefined,
      });
      onCreated();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const options: ComboboxOption[] = (channels ?? []).map((c) => ({
    value: c.token,
    label: c.name || c.token,
    description: c.channelType,
  }));

  return (
    <div className="space-y-3 rounded-md border border-dashed p-3">
      <p className="text-xs text-muted-foreground">{t('coverageCreateHelp', { severity: severityLabel })}</p>
      {loadError && <p className="text-xs text-destructive">{loadError}</p>}
      {channels != null && channels.length === 0 && (
        <p className="text-xs text-amber-600 dark:text-amber-500" data-testid="coverage-no-channels">
          {t('coverageNoChannels')}
        </p>
      )}
      {channels != null && channels.length > 0 && (
        <FormField label={t('coverageChannelLabel')} htmlFor="dr-coverage-channel">
          <Combobox
            id="dr-coverage-channel"
            value={channelToken}
            onChange={setChannelToken}
            options={options}
            allowClear={false}
            placeholder={t('coverageChannelPlaceholder')}
          />
        </FormField>
      )}
      {needsRecipients && (
        <FormField label={t('coverageRecipientsLabel')} htmlFor="dr-coverage-recipients" description={t('coverageRecipientsDescription')}>
          <Input id="dr-coverage-recipients" value={recipients} onChange={(e) => setRecipients(e.target.value)} />
        </FormField>
      )}
      {error && <p className="text-xs text-destructive">{error}</p>}
      <div className="flex gap-2">
        <Button size="sm" onClick={submit} loading={busy} disabled={busy || !ready}>
          {t('coverageCreateConfirm', { severity: severityLabel })}
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel} disabled={busy}>
          {t('common:cancel')}
        </Button>
      </div>
    </div>
  );
}
