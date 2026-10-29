// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Declare a group rollout: an AVAILABLE artifact, a device group, how many devices may be
// updating at once, and optionally how many failures pause it.
//
// The rollout is created DRAFT and tells no device anything; the detail page starts it. That
// keeps the one irreversible-looking step (freezing the group and dispatching the first
// devices) behind a button the operator presses while looking at what it will do.

import { useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { isValidToken } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { listDeviceGroupTargets } from '@/lib/api/device-management';
import { createRollout, listArtifacts } from '@/lib/api/update-management';
import '@/i18n/lazy/updates';

// The service's bounds, restated so a value it would refuse is refused before the round trip.
export const MAX_IN_FLIGHT = 1000;
const optionPageSize = 200;

function parseCount(raw: string): number | null {
  if (!/^\d+$/.test(raw.trim())) return null;
  const n = Number(raw.trim());
  return Number.isSafeInteger(n) ? n : null;
}

export function NewRolloutForm({ onCreated }: { onCreated: (token: string) => void }) {
  const { t } = useTranslation('updates');
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [artifactToken, setArtifactToken] = useState('');
  const [groupToken, setGroupToken] = useState('');
  const [maxInFlight, setMaxInFlight] = useState('10');
  const [threshold, setThreshold] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A token is spent once: a double click must not send the create twice.
  const inFlight = useRef(false);

  const artifacts = useQuery(
    () => listArtifacts({ pageNumber: 1, pageSize: optionPageSize, state: 'AVAILABLE' }).then((r) => r.results),
    [],
  );
  const groups = useQuery(() => listDeviceGroupTargets(), []);

  const artifactOptions: ComboboxOption[] = useMemo(
    () =>
      (artifacts.data ?? []).map((a) => ({
        value: a.token,
        label: `${a.name} ${a.version} (${a.component})`,
      })),
    [artifacts.data],
  );
  const groupOptions: ComboboxOption[] = useMemo(
    () => (groups.data ?? []).map((g) => ({ value: g.token, label: g.name ? `${g.name} (${g.token})` : g.token })),
    [groups.data],
  );

  const cap = parseCount(maxInFlight);
  const capOk = cap != null && cap >= 1 && cap <= MAX_IN_FLIGHT;
  const thresholdValue = threshold.trim() === '' ? undefined : parseCount(threshold);
  const thresholdOk = thresholdValue === undefined || (thresholdValue != null && thresholdValue >= 1);
  const tokenOk = isValidToken(token);
  const ready = tokenOk && !!artifactToken && !!groupToken && capOk && thresholdOk;

  const submit = async () => {
    if (!ready || inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError(null);
    try {
      const created = await createRollout({
        token,
        name: name.trim() || undefined,
        artifactToken,
        groupToken,
        maxInFlight: cap!,
        failureThreshold: thresholdValue ?? undefined,
      });
      onCreated(created.token);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      <FormField label={t('rolloutFieldToken')} htmlFor="rollout-token">
        <Input id="rollout-token" value={token} onChange={(e) => setToken(e.target.value)} />
        {token && !tokenOk && <p className="mt-1 text-sm text-destructive">{t('tokenInvalid')}</p>}
      </FormField>
      <FormField label={t('rolloutFieldName')} htmlFor="rollout-name" description={t('rolloutFieldNameHelp')}>
        <Input id="rollout-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label={t('rolloutFieldArtifact')} htmlFor="rollout-artifact" description={t('rolloutFieldArtifactHelp')}>
        <Combobox
          id="rollout-artifact"
          options={artifactOptions}
          value={artifactToken}
          onChange={setArtifactToken}
          placeholder={t('rolloutArtifactPlaceholder')}
          ariaLabel={t('rolloutFieldArtifact')}
        />
        {artifacts.error && !artifacts.data && <p className="mt-1 text-sm text-destructive">{artifacts.error}</p>}
      </FormField>
      <FormField label={t('rolloutFieldGroup')} htmlFor="rollout-group" description={t('rolloutFieldGroupHelp')}>
        <Combobox
          id="rollout-group"
          options={groupOptions}
          value={groupToken}
          onChange={setGroupToken}
          placeholder={t('rolloutGroupPlaceholder')}
          ariaLabel={t('rolloutFieldGroup')}
        />
        {groups.error && !groups.data && <p className="mt-1 text-sm text-destructive">{groups.error}</p>}
      </FormField>
      <FormField label={t('rolloutFieldMaxInFlight')} htmlFor="rollout-cap" description={t('rolloutFieldMaxInFlightHelp')}>
        <Input id="rollout-cap" inputMode="numeric" value={maxInFlight} onChange={(e) => setMaxInFlight(e.target.value)} />
        {!capOk && <p className="mt-1 text-sm text-destructive">{t('rolloutMaxInFlightInvalid', { max: MAX_IN_FLIGHT })}</p>}
      </FormField>
      <FormField label={t('rolloutFieldThreshold')} htmlFor="rollout-threshold" description={t('rolloutFieldThresholdHelp')}>
        <Input id="rollout-threshold" inputMode="numeric" value={threshold} onChange={(e) => setThreshold(e.target.value)} />
        {!thresholdOk && <p className="mt-1 text-sm text-destructive">{t('rolloutThresholdInvalid')}</p>}
      </FormField>
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <Button onClick={submit} loading={busy} disabled={!ready || busy}>
        {t('rolloutCreate')}
      </Button>
    </div>
  );
}
