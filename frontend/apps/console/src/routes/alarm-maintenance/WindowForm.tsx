// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/lazy/alarmMaintenance';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { Combobox } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { createWindow, updateWindow, type MaintenanceWindow, type AlarmSuppressionWindowUpdateRequest } from '@/lib/api/alarm-maintenance';
import type { AlarmSuppressionKind } from '@/gql/alarm-maintenance/graphql';
import { errMessage } from '@/routes/common';
import { SlotEditor } from '@/routes/maintenance-windows/SlotEditor';
import { newSlot, parseSlots, serializeSlots, slotProblems, slotsEqual, timeZoneChoices } from '@/routes/maintenance-windows/slots';
import { editable, partialUpdate, isBroad, splitKeys, localTime, isoTime, TARGET_TYPES, HOUR, type Target } from './rules';
import { SeverityField } from './SeverityField';

export function WindowForm({ entity, onDone }: { entity?: MaintenanceWindow; onDone: (token: string) => void }) {
  const { t, i18n } = useTranslation('alarmMaintenance');
  const { claims } = useAuth();
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [reason, setReason] = useState(entity?.reason ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [targets, setTargets] = useState<Target[]>(entity?.targets.map(({ type, token }) => ({ type, token })) ?? [{ type: 'DEVICE', token: '' }]);
  const [keys, setKeys] = useState(entity?.alarmKeys.join(', ') ?? '');
  const [bypass, setBypass] = useState(entity?.bypassSeverity ?? (entity ? '' : 'CRITICAL'));
  const [kind, setKind] = useState<AlarmSuppressionKind>(entity?.kind ?? 'ONE_SHOT');
  const [start, setStart] = useState(localTime(entity?.startTime ?? new Date().toISOString()));
  const [end, setEnd] = useState(localTime(entity?.endTime ?? new Date(Date.now() + HOUR).toISOString()));
  const [timezone, setTimezone] = useState(entity?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone ?? 'UTC');
  const [slots, setSlots] = useState(parseSlots(entity?.slots) ?? [newSlot()]);
  const [validFrom, setValidFrom] = useState(localTime(entity?.validFrom));
  const [validUntil, setValidUntil] = useState(localTime(entity?.validUntil));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [baseline, setBaseline] = useState(() => entity ? editable(entity) : null);
  const recurring = kind === 'RECURRING';
  const unreadable = entity?.kind === 'RECURRING' && parseSlots(entity.slots) === null;
  const valid = !unreadable && !!reason.trim() && targets.length >= 1 && targets.length <= 20 &&
    targets.every(target => target.type === 'TENANT' || target.token?.trim()) && splitKeys(keys).length <= 20 &&
    (recurring ? !!timezone && slotProblems(slots).ok && (!validFrom || !validUntil || Date.parse(validUntil) > Date.parse(validFrom)) :
      Date.parse(end) > Date.parse(start) && Date.parse(end) - Date.parse(start) <= 31 * 24 * HOUR);
  const request: AlarmSuppressionWindowUpdateRequest = {
    name: name.trim() || null, reason: reason.trim(), description: description.trim() || null,
    targets: targets.map(({ type, token }) => type === 'TENANT' ? { type } : { type, token: token?.trim() }),
    alarmKeys: splitKeys(keys), bypassSeverity: bypass || null, kind,
    startTime: recurring ? null : isoTime(start), endTime: recurring ? null : isoTime(end),
    timezone: recurring ? timezone : null, slots: recurring && slotProblems(slots).ok ? serializeSlots(slots) : null,
    validFrom: recurring ? isoTime(validFrom) : null, validUntil: recurring ? isoTime(validUntil) : null,
  };
  // Preserve the original instant (including seconds) until the operator actually edits it.
  if (entity) {
    for (const [key, draft] of [['startTime', start], ['endTime', end], ['validFrom', validFrom], ['validUntil', validUntil]] as const) {
      if (localTime(entity[key]) === draft && request[key] !== null) request[key] = entity[key];
    }
  }
  if (recurring && entity?.slots && slotsEqual(parseSlots(entity.slots) ?? [], slots)) request.slots = entity.slots;
  const broad = isBroad({ ...request, targets, kind });
  const needsBroad = broad || !!entity && isBroad(entity);
  const allowed = hasAuthority(claims, 'alarm:write') && (!needsBroad || hasAuthority(claims, 'notification:write'));
  const changes = baseline ? partialUpdate(baseline, request) : request;
  const submit = async () => {
    if (!allowed || !valid || busy) return;
    setBusy(true); setError(null);
    try {
      if (entity) { await updateWindow(entity.token, changes); setBaseline(request); onDone(entity.token); }
      else {
        const created = await createWindow({ ...request, token: token.trim(), reason: reason.trim(), targets: request.targets!, kind });
        onDone(created.token);
      }
    } catch (err) { setError(errMessage(err)); }
    finally { setBusy(false); }
  };
  return <div className="space-y-4">
    {error && <ErrorBanner message={error} />}
    {unreadable && <ErrorBanner message={t('unreadable')} />}
    {entity ? <div className="space-y-1" data-testid="alarm-maintenance-openings"><h3>{t('openings')}</h3>
      {entity.openings.map(o => <p key={o.start}>{t('opening', { start: new Date(o.start).toLocaleString(i18n.language), end: new Date(o.end).toLocaleString(i18n.language) })}</p>)}
      {!entity.openings.length && <p>{t('noOpenings')}</p>}
    </div> : <p className="text-sm text-muted-foreground">{t('previewAfterSave')}</p>}
    {!entity && <TextField id="am-token" label={t('token')} value={token} onChange={setToken} />}
    {<TextField id="am-name" label={t('name')} value={name} onChange={setName} />}
    {<TextField id="am-reason" label={t('reason')} value={reason} onChange={setReason} />}
    <FormField label={t('description')} htmlFor="am-description"><Textarea id="am-description" value={description} onChange={e => setDescription(e.target.value)} /></FormField>
    <fieldset className="space-y-2"><legend>{t('targets')}</legend>
      {targets.map((target, index) => <div key={index} className="flex flex-wrap gap-2" data-testid="maintenance-target">
        <Combobox ariaLabel={t('targetType')} value={target.type} allowClear={false}
          options={TARGET_TYPES.map(type => ({ value: type, label: t(`target${type}`) }))}
          onChange={type => setTargets(targets.map((row, i) => i === index ? { type, token: type === 'TENANT' ? undefined : '' } : row))} />
        {target.type !== 'TENANT' && <Input aria-label={t('targetToken')} value={target.token ?? ''} onChange={e => setTargets(targets.map((row, i) => i === index ? { ...row, token: e.target.value } : row))} />}
        <Button variant="ghost" onClick={() => setTargets(targets.filter((_, i) => i !== index))}>{t('removeTarget')}</Button>
      </div>)}
      <Button variant="outline" disabled={targets.length >= 20} onClick={() => setTargets([...targets, { type: 'DEVICE', token: '' }])}>{t('addTarget')}</Button>
    </fieldset>
    <FormField label={t('alarmKeys')} description={t('keysHint')} htmlFor="am-keys"><Input id="am-keys" value={keys} onChange={e => setKeys(e.target.value)} /></FormField>
    <SeverityField value={bypass} onChange={setBypass} />
    <FormField label={t('kind')}><Combobox value={kind} allowClear={false} onChange={value => setKind(value as AlarmSuppressionKind)}
      options={[{ value: 'ONE_SHOT', label: t('oneShot') }, { value: 'RECURRING', label: t('recurring') }]} /></FormField>
    {recurring ? <>
      <FormField label={t('timezone')}><Combobox value={timezone} allowClear={false} onChange={setTimezone} options={timeZoneChoices(timezone).map(value => ({ value }))} /></FormField>
      <SlotEditor translate={t} slots={slots} onChange={setSlots} help={t('slotsHint')} />
      {<TextField id="am-valid-from" label={t('validFrom')} value={validFrom} onChange={setValidFrom} type="datetime-local" />}
      {<TextField id="am-valid-until" label={t('validUntil')} value={validUntil} onChange={setValidUntil} type="datetime-local" />}
    </> : <>
      {<TextField id="am-start" label={t('startTime')} value={start} onChange={setStart} type="datetime-local" />}
      {<TextField id="am-end" label={t('endTime')} value={end} onChange={setEnd} type="datetime-local" />}
    </>}
    <p className="text-sm text-muted-foreground">{t(needsBroad ? 'broad' : 'narrow')}</p>
    {!allowed && <p role="status">{t('missingAuthority')}</p>}
    <Button loading={busy} disabled={!valid || !allowed || (!entity && !token.trim()) || (!!entity && Object.keys(changes).length === 0)} onClick={submit}>{t(entity ? 'save' : 'create')}</Button>
  </div>;
}

function TextField({ id, label, value, onChange, type = 'text' }: { id: string; label: string; value: string; onChange: (value: string) => void; type?: string }) {
  return <FormField label={label} htmlFor={id}><Input id={id} type={type} value={value} onChange={e => onChange(e.target.value)} /></FormField>;
}
