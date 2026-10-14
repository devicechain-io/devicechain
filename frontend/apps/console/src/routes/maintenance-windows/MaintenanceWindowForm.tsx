// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The maintenance-window create/edit form: token, name, description, the zone the slots are
// read in, and the weekly slots. An edit is a PARTIAL update — it names only the fields the
// operator actually changed, so two operators editing different fields never write each
// other's values back from a stale snapshot.
//
// The validation shown is a HINT (slots.ts); the server decides.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox } from '@/components/ui/combobox';
import { errMessage } from '@/routes/common';
import {
  createMaintenanceWindow,
  updateMaintenanceWindow,
  type MaintenanceWindow,
  type MaintenanceWindowUpdateRequest,
} from '@/lib/api/maintenance-windows';
import {
  newSlot,
  parseSlots,
  serializeSlots,
  slotProblems,
  slotsEqual,
  timeZoneChoices,
  type SlotDraft,
} from './slots';
import { SlotEditor } from './SlotEditor';
import { OpeningPanel } from './OpeningPanel';

function browserTimeZone(): string {
  try {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return zone && zone !== 'Local' ? zone : 'UTC';
  } catch {
    return 'UTC';
  }
}

/**
 * The update request for an edit: ONLY what differs from the stored window. Pure so the
 * "omit what did not change" rule is tested without rendering.
 */
export function windowEditRequest(
  entity: Pick<MaintenanceWindow, 'name' | 'description' | 'timezone' | 'slots'>,
  next: { name: string; description: string; timezone: string; slots: SlotDraft[] },
): MaintenanceWindowUpdateRequest {
  const request: MaintenanceWindowUpdateRequest = {};
  if (next.name.trim() !== (entity.name ?? '')) request.name = next.name.trim() || null;
  if (next.description.trim() !== (entity.description ?? '')) {
    request.description = next.description.trim() || null;
  }
  if (next.timezone !== entity.timezone) request.timezone = next.timezone;
  const stored = parseSlots(entity.slots);
  if (stored === null || !slotsEqual(stored, next.slots)) request.slots = serializeSlots(next.slots);
  return request;
}

export function MaintenanceWindowForm({
  entity,
  onDone,
}: {
  entity?: MaintenanceWindow;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const editing = entity != null;
  const storedSlots = useMemo(() => (entity ? parseSlots(entity.slots) : null), [entity]);
  // 🔴 A stored schedule this editor cannot read must not open as an empty one: a save
  // would then write the empty list over the real schedule.
  const unreadable = editing && storedSlots === null;

  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [timezone, setTimezone] = useState(entity?.timezone ?? browserTimeZone());
  const [slots, setSlots] = useState<SlotDraft[]>(storedSlots ?? [newSlot()]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const zoneOptions = useMemo(
    () => timeZoneChoices(entity?.timezone).map((z) => ({ value: z })),
    [entity?.timezone],
  );
  const hints = slotProblems(slots);

  // Only computed once the rows are valid: serialising a half-typed duration has no honest value.
  const request =
    editing && hints.ok ? windowEditRequest(entity, { name, description, timezone, slots }) : null;
  const dirty = request !== null && Object.keys(request).length > 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (editing) {
        await updateMaintenanceWindow(entity.token, request ?? {});
        onDone(t('entities:maintenanceWindowUpdatedToast', { token: entity.token }));
      } else {
        const trimmed = token.trim();
        await createMaintenanceWindow({
          token: trimmed,
          name: name.trim() || null,
          description: description.trim() || null,
          timezone,
          slots: serializeSlots(slots),
          metadata: null,
        });
        onDone(t('entities:maintenanceWindowCreatedToast', { token: trimmed }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  if (unreadable) {
    return <ErrorBanner message={t('entities:maintenanceWindowUnreadableSlots')} />;
  }

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {editing && (
        <OpeningPanel
          key={`${entity.updatedAt}|${entity.timezone}|${entity.slots}`}
          token={entity.token}
        />
      )}
      <FormField
        label={t('common:colToken')}
        htmlFor="mw-token"
        description={editing ? t('entities:maintenanceWindowTokenFixed') : undefined}
      >
        {editing ? (
          <Input id="mw-token" value={token} disabled />
        ) : (
          <TokenField
            id="mw-token"
            entityType="maintenance-window"
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={t('entities:maintenanceWindowTokenPlaceholder')}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="mw-name">
        <Input id="mw-name" value={name} onChange={(ev) => setName(ev.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="mw-description">
        <Textarea
          id="mw-description"
          value={description}
          onChange={(ev) => setDescription(ev.target.value)}
        />
      </FormField>
      <FormField
        label={t('entities:maintenanceWindowTimezone')}
        htmlFor="mw-timezone"
        description={t('entities:maintenanceWindowTimezoneHelp')}
      >
        <Combobox
          id="mw-timezone"
          options={zoneOptions}
          value={timezone}
          onChange={setTimezone}
          allowClear={false}
        />
      </FormField>
      <FormField label={t('entities:maintenanceWindowSlots')}>
        <SlotEditor slots={slots} onChange={setSlots} disabled={busy} />
      </FormField>
      <div className="flex gap-2">
        <Button
          onClick={submit}
          loading={busy}
          disabled={busy || !hints.ok || (editing ? !dirty : !token.trim())}
        >
          {editing ? t('common:saveChanges') : t('entities:maintenanceWindowCreateAction')}
        </Button>
      </div>
    </div>
  );
}
