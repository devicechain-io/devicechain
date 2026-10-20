// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Backfill dialog of a device profile version: which version, which calculated metrics, which
// range, which devices. A backfill computes a calculated metric for stored readings that have no
// value for it; it never changes a value that is stored. Like every other form of this console it
// is a right-side drawer.
//
// Nothing here knows whether the worker is running: only starting a job says so (UNAVAILABLE). The
// panel remembers that answer and hands it back as `unavailable`, which turns both start buttons
// off and states the reason; the control is never hidden for it.

import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { errorCode } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { Input } from '@/components/ui/input';
import { Select, SelectItem } from '@/components/ui/select';
import { Textarea } from '@/components/ui/textarea';
import { FormDrawer } from '@/components/registry';
import { useToast } from '@/components/ui/toast';
import { errMessage } from '@/routes/common';
import { useQuery } from '@/lib/hooks/use-query';
import { getCalculatedMetricPlan, listDeviceProfileVersions } from '@/lib/api/device-management';
import { startCalculatedMetricBackfill } from '@/lib/api/event-management';
import {
  BACKFILL_MAX_DAYS,
  BACKFILL_MAX_DEVICES,
  backfillRangeProblem,
  defaultBackfillRange,
  parseDeviceTokens,
  toLocalInput,
} from './backfillRange';

export function BackfillDrawer({
  profileToken,
  activeVersion,
  initialVersion,
  open,
  onOpenChange,
  unavailable,
  onUnavailable,
  onStarted,
}: {
  profileToken: string;
  activeVersion: number | null;
  /** The version to open on (the one just published); the active version when omitted. */
  initialVersion?: number | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Why starting a job is not possible on this instance right now; null when it is. */
  unavailable: string | null;
  onUnavailable: (reason: string) => void;
  /** Called with the new job's token once it is queued. */
  onStarted: (token: string) => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const { toast } = useToast();
  const [version, setVersion] = useState<number | null>(initialVersion ?? activeVersion);
  const [picked, setPicked] = useState<ReadonlySet<string> | null>(null);
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [devices, setDevices] = useState('');
  const [oneDevice, setOneDevice] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Each time the drawer opens it starts from its defaults again.
  useEffect(() => {
    if (!open) return;
    setVersion(initialVersion ?? activeVersion);
    setPicked(null);
    setDevices('');
    setOneDevice('');
    setError(null);
  }, [open, initialVersion, activeVersion]);

  const versions = useQuery(
    () => (open ? listDeviceProfileVersions(profileToken) : Promise.resolve(null)),
    [open, profileToken],
  );
  const plan = useQuery(
    () => (open && version != null ? getCalculatedMetricPlan(profileToken, version) : Promise.resolve(null)),
    [open, profileToken, version],
  );
  // useQuery keeps the previous answer while the next loads, and after a failed load. A plan
  // of another version must not be started with: its keys and range belong to that version.
  const current = plan.data != null && plan.data.version === version ? plan.data : null;
  const calculated = useMemo(() => current?.calculated ?? [], [current]);

  // The defaults follow the plan of the chosen version: the keys that are new in it, and a range
  // ending where the live path took over.
  useEffect(() => {
    if (!current) return;
    const r = defaultBackfillRange(current.activeSince, new Date());
    setFrom(toLocalInput(r.from));
    setTo(toLocalInput(r.to));
    setPicked(new Set(current.calculated.filter((m) => m.newInVersion).map((m) => m.key)));
  }, [current]);

  const selected = picked ?? new Set<string>();
  const range = backfillRangeProblem(new Date(from), new Date(to), new Date());
  const deviceTokens = parseDeviceTokens(devices);
  const tooManyDevices = deviceTokens.length > BACKFILL_MAX_DEVICES;
  const ready = !busy && !unavailable && current != null && selected.size > 0 && range === null;

  const toggle = (key: string, on: boolean) => {
    const next = new Set(selected);
    if (on) next.add(key);
    else next.delete(key);
    setPicked(next);
  };

  const start = async (tokens: string[]) => {
    if (version == null) return;
    setBusy(true);
    setError(null);
    try {
      const token = await startCalculatedMetricBackfill({
        profileToken,
        version,
        metricKeys: calculated.filter((m) => selected.has(m.key)).map((m) => m.key),
        from: new Date(from).toISOString(),
        to: new Date(to).toISOString(),
        deviceTokens: tokens.length > 0 ? tokens : undefined,
      });
      toast(t('backfillStartedToast'));
      onStarted(token);
      onOpenChange(false);
    } catch (err) {
      if (errorCode(err) === 'UNAVAILABLE') onUnavailable(errMessage(err));
      else setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const one = oneDevice.trim();

  return (
    <FormDrawer
      shot="calculated-backfill"
      open={open}
      onOpenChange={onOpenChange}
      title={t('backfillTitle')}
      description={t('backfillDescription')}
    >
      <div className="space-y-5">
        {unavailable && (
          <p role="alert" className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm">
            {t('backfillUnavailable', { reason: unavailable })}
          </p>
        )}

        <FormField label={t('backfillVersion')} htmlFor="bf-version">
          <Select
            id="bf-version"
            value={version == null ? '' : String(version)}
            onChange={(v) => setVersion(Number(v))}
            placeholder={t('backfillVersionPlaceholder')}
          >
            {(versions.data ?? []).map((v) => (
              <SelectItem key={v.version} value={String(v.version)}>
                {v.version === activeVersion
                  ? t('backfillVersionActive', { version: v.version })
                  : t('backfillVersionOption', { version: v.version })}
              </SelectItem>
            ))}
          </Select>
        </FormField>

        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">{t('backfillMetrics')}</legend>
          <HintText size="md">{t('backfillMetricsHint')}</HintText>
          {plan.error && <p className="text-sm text-destructive">{plan.error}</p>}
          {current && calculated.length === 0 && (
            <p className="text-sm text-muted-foreground">{t('backfillNoCalculated')}</p>
          )}
          {calculated.map((m) => (
            <div key={m.key} className="space-y-0.5">
              <label className="flex items-center gap-2 text-sm">
                <Checkbox
                  checked={selected.has(m.key)}
                  onCheckedChange={(c) => toggle(m.key, c === true)}
                  aria-label={m.key}
                />
                <span className="font-mono">{m.key}</span>
                {m.newInVersion && <span className="text-xs text-muted-foreground">{t('backfillMetricNew')}</span>}
              </label>
              {m.changedSinceEarlier && (
                <p className="ml-6 text-xs text-amber-600 dark:text-amber-500" data-testid={`bf-changed-${m.key}`}>
                  {t('backfillMetricChanged')}
                </p>
              )}
            </div>
          ))}
        </fieldset>

        <div className="space-y-2">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <FormField label={t('backfillFrom')} htmlFor="bf-from">
              <Input id="bf-from" type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
            </FormField>
            <FormField label={t('backfillTo')} htmlFor="bf-to">
              <Input id="bf-to" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
            </FormField>
          </div>
          <HintText size="md">{t('backfillRangeHint', { days: BACKFILL_MAX_DAYS })}</HintText>
          {range && (
            <p role="alert" className="text-sm text-destructive">
              {t(`backfillRangeProblem.${range}`, { days: BACKFILL_MAX_DAYS })}
            </p>
          )}
        </div>

        <FormField
          label={t('backfillDevices')}
          htmlFor="bf-devices"
          description={t('backfillDevicesHint', { max: BACKFILL_MAX_DEVICES })}
        >
          <Textarea id="bf-devices" value={devices} onChange={(e) => setDevices(e.target.value)} />
        </FormField>
        {tooManyDevices && (
          <p role="alert" className="text-sm text-destructive">
            {t('backfillTooManyDevices', { max: BACKFILL_MAX_DEVICES })}
          </p>
        )}

        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}

        <div className="space-y-2 rounded-md border bg-muted/30 p-3">
          <p className="text-sm font-medium">{t('backfillTryOneTitle')}</p>
          <HintText size="md">{t('backfillTryOneHint')}</HintText>
          <div className="flex flex-wrap items-end gap-2">
            <FormField label={t('backfillTryOneDevice')} htmlFor="bf-one">
              <Input
                id="bf-one"
                value={oneDevice}
                onChange={(e) => setOneDevice(e.target.value)}
                placeholder={t('backfillTryOnePlaceholder')}
              />
            </FormField>
            <Button disabled={!ready || !one} loading={busy} onClick={() => start([one])}>
              {t('backfillTryOne')}
            </Button>
          </div>
        </div>

        <Button variant="outline" disabled={!ready || tooManyDevices} loading={busy} onClick={() => start(deviceTokens)}>
          {deviceTokens.length > 0 ? t('backfillStartSome', { count: deviceTokens.length }) : t('backfillStartAll')}
        </Button>
      </div>
    </FormDrawer>
  );
}
