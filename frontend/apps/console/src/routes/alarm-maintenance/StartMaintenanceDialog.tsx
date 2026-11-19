// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/lazy/alarmMaintenance';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { FormDrawer } from '@/components/registry/FormDrawer';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { createWindow } from '@/lib/api/alarm-maintenance';
import { errMessage } from '@/routes/common';
import { HOUR, isBroad, validKeys, type Target } from './rules';
import { SeverityField } from './SeverityField';
import { AlarmKeysEditor } from './AlarmKeysEditor';

const PRESETS = ['30', '60', '120', '240', 'custom'];
export default function StartMaintenanceDialog({ target, onClose, onDone }: {
  target: Target; onClose: () => void; onDone: () => void;
}) {
  const { t } = useTranslation('alarmMaintenance');
  const { claims } = useAuth();
  const [preset, setPreset] = useState('60');
  const [custom, setCustom] = useState('');
  const [reason, setReason] = useState('');
  const [keys, setKeys] = useState<string[]>([]);
  const [bypass, setBypass] = useState('CRITICAL');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const now = Date.now();
  const end = preset === 'custom' ? Date.parse(custom) : now + Number(preset) * 60000;
  const broad = isBroad({ targets: [target], kind: 'ONE_SHOT', startTime: new Date(now).toISOString(), endTime: Number.isFinite(end) ? new Date(end).toISOString() : null });
  const allowed = hasAuthority(claims, 'alarm:write') && (!broad || hasAuthority(claims, 'notification:write'));
  const valid = reason.trim() && validKeys(keys) && end > now && end - now <= 31 * 24 * HOUR;
  const submit = async () => {
    if (!allowed || !valid || busy) return;
    setBusy(true); setError(null);
    try {
      const start = Date.now();
      await createWindow({ token: crypto.randomUUID(), reason: reason.trim(), targets: [target],
        kind: 'ONE_SHOT', startTime: new Date(start).toISOString(),
        endTime: new Date(preset === 'custom' ? end : start + Number(preset) * 60000).toISOString(),
        alarmKeys: keys, bypassSeverity: bypass || null });
      onDone();
    } catch (err) { setError(errMessage(err)); }
    finally { setBusy(false); }
  };
  return <FormDrawer open onOpenChange={open => { if (!open) onClose(); }} title={t('start')} shot="alarm-maintenance-start">
    <div className="space-y-4">
      {error && <ErrorBanner message={error} />}
      <SegmentedControl value={preset} onValueChange={setPreset} ariaLabel={t('duration')}
        options={PRESETS.map(value => ({ value, label: t(`preset${value}`) }))} />
      {preset === 'custom' && <FormField label={t('endTime')} htmlFor="am-custom"><Input id="am-custom" type="datetime-local" value={custom} onChange={e => setCustom(e.target.value)} /></FormField>}
      <FormField label={t('reason')} htmlFor="am-reason"><Input id="am-reason" value={reason} onChange={e => setReason(e.target.value)} required /></FormField>
      <AlarmKeysEditor keys={keys} onChange={setKeys} />
      <SeverityField value={bypass} onChange={setBypass} />
      <p className="text-sm text-muted-foreground">{t(broad ? 'broad' : 'narrow')}</p>
      {!allowed && <p role="status">{t('missingAuthority')}</p>}
      <Button onClick={submit} loading={busy} disabled={!allowed || !valid}>{t('start')}</Button>
    </div>
  </FormDrawer>;
}
