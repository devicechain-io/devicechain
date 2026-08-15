// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "Appearance" tab for registry types: edits the background / text / border
// colors and icon that drive the type's capsule everywhere it appears. It sends
// those four fields and nothing else — reconciling them with a family's update
// contract is the caller's job, since the four families sharing this form are no
// longer all on the same one. The parent reloads so the Basic tab stays in sync.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useToast } from '@/components/ui/toast';
import { ColorPicker } from '@/components/ui/color-picker';
import { ToggleButton } from '@/components/ui/toggle-button';
import { errMessage } from '@/routes/common';
import { TypeCapsule } from '@/components/TypeCapsule';
import { TYPE_ICONS, TYPE_ICON_KEYS } from '@/lib/type-icons';

// What this form READS off the entity. The token and name are not edited here —
// they feed the live capsule preview, which has to look like the real thing.
export interface AppearanceSource {
  token: string;
  name: string | null;
  icon: string | null;
  backgroundColor: string | null;
  foregroundColor: string | null;
  borderColor: string | null;
}

// 🔴 What this form SENDS: the four fields it edits, and NOTHING else. The
// carry-forward belongs to the caller, because only the caller knows its family's
// update contract — a full-replace family spreads these over its
// `…Preserved(entity)` projection, and a partial-update family passes them through
// untouched. A form that carried `name` itself would be right for one and a
// lost-update for the other: it would write a name read when the tab was opened,
// over whatever the Identity tab saved since.
//
// 🔴 Every field is REQUIRED and non-undefined, deliberately. Spread over a
// preserved projection, an optional field here would arrive as `undefined` and
// clear the preserved value — the same deletion by omission the projection exists
// to stop, one layer further in.
export interface AppearanceEdits {
  icon: string | null;
  backgroundColor: string | null;
  foregroundColor: string | null;
  borderColor: string | null;
}

function ColorField({
  label,
  value,
  fallback,
  onChange,
}: {
  label: string;
  value: string | undefined;
  fallback: string;
  onChange: (v: string | undefined) => void;
}) {
  const { t } = useTranslation('common');
  return (
    <FormField label={label} htmlFor={`color-${label}`}>
      <div className="flex items-center gap-2">
        <ColorPicker
          id={`color-${label}`}
          value={value ?? fallback}
          fallback={fallback}
          ariaLabel={label}
          onChange={onChange}
          className="w-12"
        />
        <span className="font-mono text-xs text-muted-foreground">{value ?? '—'}</span>
        {value && (
          <Button
            variant="quiet"
            size="inline"
            className="ml-auto text-xs"
            onClick={() => onChange(undefined)}
          >
            {t('clear')}
          </Button>
        )}
      </div>
    </FormField>
  );
}

export function TypeAppearanceForm({
  entity,
  update,
  onSaved,
}: {
  entity: AppearanceSource;
  update: (req: AppearanceEdits) => Promise<unknown>;
  onSaved: () => void;
}) {
  const { t } = useTranslation('common');
  const { toast } = useToast();
  const [icon, setIcon] = useState<string | undefined>(entity.icon ?? undefined);
  const [backgroundColor, setBackground] = useState<string | undefined>(
    entity.backgroundColor ?? undefined,
  );
  const [foregroundColor, setForeground] = useState<string | undefined>(
    entity.foregroundColor ?? undefined,
  );
  const [borderColor, setBorder] = useState<string | undefined>(entity.borderColor ?? undefined);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      await update({
        icon: icon ?? null,
        backgroundColor: backgroundColor ?? null,
        foregroundColor: foregroundColor ?? null,
        borderColor: borderColor ?? null,
      });
      toast(t('appearanceSaved'));
      onSaved();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}

      <FormField label={t('appearancePreview')}>
        <div>
          <TypeCapsule
            appearance={{ token: entity.token, name: entity.name, icon, backgroundColor, foregroundColor, borderColor }}
          />
        </div>
      </FormField>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <ColorField label={t('colorBackground')} value={backgroundColor} fallback="#1f2937" onChange={setBackground} />
        <ColorField label={t('colorText')} value={foregroundColor} fallback="#f9fafb" onChange={setForeground} />
        <ColorField label={t('colorBorder')} value={borderColor} fallback="#374151" onChange={setBorder} />
      </div>

      <FormField label={t('appearanceIcon')} description={t('iconPickerHint')}>
        <div className="grid grid-cols-9 gap-1 sm:grid-cols-12">
          {TYPE_ICON_KEYS.map((key) => {
            const Icon = TYPE_ICONS[key];
            const selected = icon === key;
            return (
              // Clicking the chosen icon CLEARS it, so this is a set of independent
              // toggles rather than a radio group — a radio group has no way to end up
              // with nothing selected.
              <ToggleButton
                key={key}
                aria-label={key}
                pressed={selected}
                onClick={() => setIcon(selected ? undefined : key)}
                className={cn(
                  'flex aspect-square items-center justify-center rounded-md border text-foreground',
                  selected
                    ? 'border-primary bg-primary/10 text-primary'
                    : 'border-border hover:bg-accent hover:text-accent-foreground',
                )}
              >
                <Icon size={16} />
              </ToggleButton>
            );
          })}
        </div>
      </FormField>

      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy}>
          {t('saveAppearance')}
        </Button>
      </div>
    </div>
  );
}
