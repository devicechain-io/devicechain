// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The weekly slot editor of a maintenance window: one row per opening, each a day, a
// wall-clock start time in the window's zone, and a duration in elapsed minutes. A row may
// run past midnight and past the end of the week — the editor says so and does not try to
// split it.
//
// The flags it draws are HINTS (see slots.ts); the server decides what is valid.

import { useTranslation } from 'react-i18next';
import { Plus, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
import { HintText } from '@/components/ui/hint-text';
import {
  DAYS,
  MAX_SLOTS,
  MAX_DURATION_MINUTES,
  newSlot,
  parseDuration,
  slotProblems,
  splitDuration,
  type Day,
  type SlotDraft,
} from './slots';

export function SlotEditor({
  slots,
  onChange,
  disabled,
  help,
}: {
  slots: SlotDraft[];
  onChange: (next: SlotDraft[]) => void;
  disabled?: boolean;
  /** Replaces the default helper text, for a caller whose slots mean something other than a window. */
  help?: string;
}) {
  const { t } = useTranslation('entities');
  const hints = slotProblems(slots);

  const patch = (i: number, change: Partial<SlotDraft>) =>
    onChange(slots.map((s, j) => (j === i ? { ...s, ...change } : s)));

  const dayOptions = DAYS.map((d) => ({ value: d, label: t(`maintenanceWindowDay.${d}`) }));

  return (
    <div className="space-y-2" data-testid="slot-editor">
      {slots.map((slot, i) => {
        const problems = hints.rows.get(i) ?? [];
        const minutes = parseDuration(slot.duration);
        const readable =
          minutes !== null && !problems.includes('duration') ? splitDuration(minutes) : null;
        return (
          <div key={i} className="flex flex-wrap items-start gap-2" data-testid="slot-row">
            <div className="w-40">
              <Combobox
                options={dayOptions}
                value={slot.day}
                onChange={(v) => patch(i, { day: v as Day })}
                allowClear={false}
                disabled={disabled}
                ariaLabel={t('maintenanceWindowSlotDay')}
              />
            </div>
            <div className="w-32">
              <Input
                type="time"
                value={slot.start}
                onChange={(ev) => patch(i, { start: ev.target.value })}
                disabled={disabled}
                aria-label={t('maintenanceWindowSlotStart')}
                aria-invalid={problems.includes('start') || undefined}
              />
            </div>
            <div className="w-36">
              <Input
                type="number"
                inputMode="numeric"
                min={1}
                max={MAX_DURATION_MINUTES}
                value={slot.duration}
                onChange={(ev) => patch(i, { duration: ev.target.value })}
                disabled={disabled}
                aria-label={t('maintenanceWindowSlotDuration')}
                aria-invalid={problems.includes('duration') || undefined}
              />
            </div>
            <span
              className="text-muted-foreground flex h-10 min-w-24 items-center text-sm tabular-nums"
              data-testid="slot-duration-readout"
            >
              {readable
                ? t('maintenanceWindowDurationHm', { hours: readable.hours, minutes: readable.minutes })
                : ''}
            </span>
            {!disabled && (
              <Button
                variant="ghost"
                size="sm"
                className="h-10"
                onClick={() => onChange(slots.filter((_, j) => j !== i))}
                aria-label={t('maintenanceWindowSlotRemove')}
              >
                <Trash2 size={14} />
              </Button>
            )}
            {problems.length > 0 && (
              <p className="text-destructive basis-full text-xs" data-testid="slot-problem">
                {problems.includes('start') && t('maintenanceWindowHintStart')}
                {problems.length === 2 && ' '}
                {problems.includes('duration') &&
                  t('maintenanceWindowHintDuration', { max: MAX_DURATION_MINUTES })}
              </p>
            )}
          </div>
        );
      })}
      <div className="flex flex-wrap items-center gap-3">
        {!disabled && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => onChange([...slots, newSlot()])}
            disabled={slots.length >= MAX_SLOTS}
          >
            <Plus size={14} /> {t('maintenanceWindowSlotAdd')}
          </Button>
        )}
        <span className="text-muted-foreground text-xs" data-testid="slot-count">
          {t('maintenanceWindowSlotCount', { count: slots.length, max: MAX_SLOTS })}
        </span>
      </div>
      {hints.count === 'tooFew' && (
        <p className="text-destructive text-xs" data-testid="slot-count-problem">
          {t('maintenanceWindowHintTooFew')}
        </p>
      )}
      {hints.count === 'tooMany' && (
        <p className="text-destructive text-xs" data-testid="slot-count-problem">
          {t('maintenanceWindowHintTooMany', { max: MAX_SLOTS })}
        </p>
      )}
      <HintText size="md">{help ?? t('maintenanceWindowSlotsHelp')}</HintText>
    </div>
  );
}
