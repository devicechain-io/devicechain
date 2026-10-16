// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The notification-policy create/edit form: name, description, whether the policy is
// enabled, its throttle and escalation numbers, and its QUIET HOURS. An edit is a PARTIAL
// update that names only what the operator changed and passes the `updatedAt` it read, so a
// concurrent edit is refused instead of overwritten.
//
// Routing RULES are not edited here. They are shown read-only, and `rules` is never sent on an
// update, which leaves the stored rules (and their ids) untouched.
//
// The quiet-hours validation shown is a HINT (slots.ts); the server decides.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox } from '@/components/ui/combobox';
import { Select, SelectItem } from '@/components/ui/select';
import { HintText } from '@/components/ui/hint-text';
import { errMessage } from '@/routes/common';
import {
  createNotificationPolicy,
  updateNotificationPolicy,
  type NotificationPolicyRecord,
} from '@/lib/api/notifications';
import {
  parseSlots,
  slotCoverageProblem,
  slotProblems,
  timeZoneChoices,
  type SlotDraft,
} from '@/routes/maintenance-windows/slots';
import { SlotEditor } from '@/routes/maintenance-windows/SlotEditor';
import {
  BYPASS_SEVERITIES,
  DEFAULT_BYPASS_SEVERITY,
  hasQuietHours,
  newQuietSlot,
  parseCount,
  policyCreateRequest,
  policyEditRequest,
  type PolicyDraft,
} from './policyRequest';

function browserTimeZone(): string {
  try {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return zone && zone !== 'Local' ? zone : 'UTC';
  } catch {
    return 'UTC';
  }
}

function recipientsOf(raw: string | null): string {
  if (!raw) return '';
  try {
    const v: unknown = JSON.parse(raw);
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string').join(', ') : raw;
  } catch {
    return raw;
  }
}

const countText = (n: number | null | undefined) => (n == null ? '' : String(n));

export function NotificationPolicyForm({
  entity,
  onDone,
}: {
  entity?: NotificationPolicyRecord;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['entities', 'common', 'alarms']);
  const editing = entity != null;
  const storedQuiet = editing && hasQuietHours(entity);
  const storedSlots = useMemo(() => (entity ? parseSlots(entity.quietHoursSlots) : null), [entity]);
  // 🔴 A stored schedule this editor cannot read must not open as an empty one: a save would
  // write the empty list over the real schedule. The section is replaced by a notice and the
  // request builder never names the quiet-hours fields.
  const unreadable = storedQuiet && storedSlots === null;

  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [enabled, setEnabled] = useState(entity?.enabled ?? true);
  const [throttle, setThrottle] = useState(countText(entity?.throttleSeconds));
  const [escalate, setEscalate] = useState(countText(entity?.escalateAfterSeconds));
  const [maxEsc, setMaxEsc] = useState(countText(entity?.maxEscalations));
  const [quietOn, setQuietOn] = useState(storedQuiet);
  const [timezone, setTimezone] = useState(entity?.quietHoursTimezone ?? browserTimeZone());
  const [slots, setSlots] = useState<SlotDraft[]>(storedSlots ?? [newQuietSlot()]);
  const [bypass, setBypass] = useState(entity?.quietHoursBypassSeverity ?? '');
  const [bypassTouched, setBypassTouched] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const zoneOptions = useMemo(
    () => timeZoneChoices(entity?.quietHoursTimezone).map((z) => ({ value: z })),
    [entity?.quietHoursTimezone],
  );

  const toggleQuiet = (on: boolean) => {
    setQuietOn(on);
    // The first time quiet hours go on, let the worst alarms through: a silent overnight
    // policy that also holds CRITICAL is rarely what was meant.
    if (on && !storedQuiet && !bypassTouched && bypass === '') setBypass(DEFAULT_BYPASS_SEVERITY);
  };

  const hints = slotProblems(slots);
  const coverage = hints.ok ? slotCoverageProblem(slots) : null;
  const quietValid = !quietOn || unreadable || (hints.ok && coverage === null && timezone !== '');
  const numbersValid = [throttle, escalate, maxEsc].every((x) => parseCount(x) !== undefined);

  const draft: PolicyDraft = {
    name,
    description,
    enabled,
    throttleSeconds: throttle,
    escalateAfterSeconds: escalate,
    maxEscalations: maxEsc,
    quietHoursOn: quietOn,
    timezone,
    slots,
    bypassSeverity: bypass,
  };
  // Only computed once the inputs are valid: serialising a half-typed duration has no honest value.
  const request = editing && quietValid && numbersValid ? policyEditRequest(entity, draft) : null;
  const dirty = request !== null && Object.keys(request).length > 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (editing) {
        await updateNotificationPolicy(entity.token, request ?? {}, entity.updatedAt);
        onDone(t('entities:notificationPolicyUpdatedToast', { token: entity.token }));
      } else {
        const trimmed = token.trim();
        await createNotificationPolicy(policyCreateRequest(trimmed, draft));
        onDone(t('entities:notificationPolicyCreatedToast', { token: trimmed }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const sevLabel = (s: string) =>
    s === '*'
      ? t('entities:notificationPolicyAnySeverity')
      : t(`alarms:sev${s.charAt(0)}${s.slice(1).toLowerCase()}`, { defaultValue: s });

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label={t('common:colToken')}
        htmlFor="np-token"
        description={editing ? t('entities:notificationPolicyTokenFixed') : undefined}
      >
        {editing ? (
          <Input id="np-token" value={token} disabled />
        ) : (
          <TokenField
            id="np-token"
            entityType="notification-policy"
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={t('entities:notificationPolicyTokenPlaceholder')}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="np-name">
        <Input id="np-name" value={name} onChange={(ev) => setName(ev.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="np-description">
        <Textarea
          id="np-description"
          value={description}
          onChange={(ev) => setDescription(ev.target.value)}
        />
      </FormField>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox checked={enabled} onCheckedChange={(c) => setEnabled(c === true)} />
        <span>{t('common:enabled')}</span>
        <span className="text-muted-foreground">{t('entities:notificationPolicyEnabledHint')}</span>
      </label>
      <div className="grid gap-4 sm:grid-cols-3">
        <FormField
          label={t('entities:notificationPolicyThrottle')}
          htmlFor="np-throttle"
          description={t('entities:notificationPolicyThrottleHelp')}
        >
          <Input
            id="np-throttle"
            type="number"
            inputMode="numeric"
            min={0}
            value={throttle}
            onChange={(ev) => setThrottle(ev.target.value)}
          />
        </FormField>
        <FormField
          label={t('entities:notificationPolicyEscalateAfter')}
          htmlFor="np-escalate"
          description={t('entities:notificationPolicyEscalateAfterHelp')}
        >
          <Input
            id="np-escalate"
            type="number"
            inputMode="numeric"
            min={0}
            value={escalate}
            onChange={(ev) => setEscalate(ev.target.value)}
          />
        </FormField>
        <FormField
          label={t('entities:notificationPolicyMaxEscalations')}
          htmlFor="np-max-escalations"
          description={t('entities:notificationPolicyMaxEscalationsHelp')}
        >
          <Input
            id="np-max-escalations"
            type="number"
            inputMode="numeric"
            min={0}
            value={maxEsc}
            onChange={(ev) => setMaxEsc(ev.target.value)}
          />
        </FormField>
      </div>

      <section className="space-y-3" data-testid="quiet-hours">
        <h3 className="text-sm font-semibold">{t('entities:notificationPolicyQuietHours')}</h3>
        {unreadable ? (
          <ErrorBanner message={t('entities:notificationPolicyUnreadableQuietHours')} />
        ) : (
          <>
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={quietOn}
                onCheckedChange={(c) => toggleQuiet(c === true)}
                disabled={busy}
                aria-label={t('entities:notificationPolicyQuietHoursOn')}
              />
              <span>{t('entities:notificationPolicyQuietHoursOn')}</span>
            </label>
            <HintText size="md">{t('entities:notificationPolicyQuietHoursHelp')}</HintText>
            {quietOn && (
              <div className="space-y-4">
                <FormField
                  label={t('entities:notificationPolicyQuietTimezone')}
                  htmlFor="np-quiet-timezone"
                  description={t('entities:notificationPolicyQuietTimezoneHelp')}
                >
                  <Combobox
                    id="np-quiet-timezone"
                    options={zoneOptions}
                    value={timezone}
                    onChange={setTimezone}
                    allowClear={false}
                  />
                </FormField>
                <FormField label={t('entities:notificationPolicyQuietSlots')}>
                  <SlotEditor
                    slots={slots}
                    onChange={setSlots}
                    disabled={busy}
                    help={t('entities:notificationPolicyQuietSlotsHelp')}
                  />
                  {coverage === 'overlap' && (
                    <p className="text-destructive mt-1 text-xs" data-testid="quiet-coverage-problem">
                      {t('entities:notificationPolicyHintOverlap')}
                    </p>
                  )}
                  {coverage === 'wholeWeek' && (
                    <p className="text-destructive mt-1 text-xs" data-testid="quiet-coverage-problem">
                      {t('entities:notificationPolicyHintWholeWeek')}
                    </p>
                  )}
                </FormField>
                <FormField
                  label={t('entities:notificationPolicyBypass')}
                  htmlFor="np-bypass"
                  description={t('entities:notificationPolicyBypassHelp')}
                >
                  <Select
                    id="np-bypass"
                    value={bypass}
                    onChange={(v) => {
                      setBypassTouched(true);
                      setBypass(v);
                    }}
                  >
                    <SelectItem value="">{t('entities:notificationPolicyBypassNone')}</SelectItem>
                    {BYPASS_SEVERITIES.map((s) => (
                      <SelectItem key={s} value={s}>
                        {sevLabel(s)}
                      </SelectItem>
                    ))}
                  </Select>
                </FormField>
              </div>
            )}
          </>
        )}
      </section>

      <section className="space-y-2" data-testid="policy-rules">
        <h3 className="text-sm font-semibold">{t('entities:notificationPolicyRules')}</h3>
        {entity && entity.rules.length > 0 ? (
          <ul className="space-y-1 text-sm">
            {entity.rules.map((r) => (
              <li key={r.id} data-testid="policy-rule">
                <span className="font-medium">{sevLabel(r.severity)}</span>
                <span className="text-muted-foreground"> → </span>
                <span className="font-mono text-xs">
                  {r.channel?.token ?? t('entities:notificationPolicyNoChannel')}
                </span>
                {recipientsOf(r.recipients) && (
                  <span className="text-muted-foreground"> ({recipientsOf(r.recipients)})</span>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground text-sm">{t('entities:notificationPolicyRulesNone')}</p>
        )}
        <HintText size="md">{t('entities:notificationPolicyRulesHelp')}</HintText>
      </section>

      <div className="flex gap-2">
        <Button
          onClick={submit}
          loading={busy}
          disabled={busy || !quietValid || !numbersValid || (editing ? !dirty : !token.trim())}
        >
          {editing ? t('common:saveChanges') : t('entities:notificationPolicyCreateAction')}
        </Button>
      </div>
    </div>
  );
}
