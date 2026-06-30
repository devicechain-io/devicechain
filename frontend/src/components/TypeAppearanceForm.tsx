// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "Appearance" tab for registry types: edits the background / text / border
// colors and icon that drive the type's capsule everywhere it appears. Saves the
// full type (name + description preserved from the entity) since the type update
// is a full replace; the parent reloads so the Basic tab stays in sync.

import { useState } from 'react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useToast } from '@/components/ui/toast';
import { errMessage } from '@/routes/common';
import { TypeCapsule } from '@/components/TypeCapsule';
import { TYPE_ICONS, TYPE_ICON_KEYS } from '@/lib/type-icons';

// The shape a type appearance update sends — structurally a <X>TypeCreateRequest.
export interface AppearanceUpdate {
  token: string;
  name?: string;
  description?: string;
  icon?: string;
  backgroundColor?: string;
  foregroundColor?: string;
  borderColor?: string;
}

interface AppearanceEntity {
  token: string;
  name?: string | null;
  description?: string | null;
  icon?: string | null;
  backgroundColor?: string | null;
  foregroundColor?: string | null;
  borderColor?: string | null;
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
  return (
    <FormField label={label} htmlFor={`color-${label}`}>
      <div className="flex items-center gap-2">
        <input
          id={`color-${label}`}
          type="color"
          value={value ?? fallback}
          onChange={(e) => onChange(e.target.value)}
          className="h-9 w-12 shrink-0 cursor-pointer rounded border border-input bg-background"
        />
        <span className="font-mono text-xs text-muted-foreground">{value ?? '—'}</span>
        {value && (
          <button
            type="button"
            onClick={() => onChange(undefined)}
            className="ml-auto text-xs text-muted-foreground hover:text-foreground"
          >
            Clear
          </button>
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
  entity: AppearanceEntity;
  update: (req: AppearanceUpdate) => Promise<unknown>;
  onSaved: () => void;
}) {
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
        token: entity.token,
        name: entity.name ?? undefined,
        description: entity.description ?? undefined,
        icon,
        backgroundColor,
        foregroundColor,
        borderColor,
      });
      toast('Appearance saved');
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

      <FormField label="Preview">
        <div>
          <TypeCapsule
            appearance={{ token: entity.token, name: entity.name, icon, backgroundColor, foregroundColor, borderColor }}
          />
        </div>
      </FormField>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <ColorField label="Background" value={backgroundColor} fallback="#1f2937" onChange={setBackground} />
        <ColorField label="Text" value={foregroundColor} fallback="#f9fafb" onChange={setForeground} />
        <ColorField label="Border" value={borderColor} fallback="#374151" onChange={setBorder} />
      </div>

      <FormField label="Icon" description="Pick an icon, or click the selected one again to clear it.">
        <div className="grid grid-cols-9 gap-1 sm:grid-cols-12">
          {TYPE_ICON_KEYS.map((key) => {
            const Icon = TYPE_ICONS[key];
            const selected = icon === key;
            return (
              <button
                key={key}
                type="button"
                aria-label={key}
                aria-pressed={selected}
                onClick={() => setIcon(selected ? undefined : key)}
                className={cn(
                  'flex aspect-square items-center justify-center rounded-md border text-foreground transition-colors',
                  selected
                    ? 'border-primary bg-primary/10 text-primary'
                    : 'border-border hover:bg-accent hover:text-accent-foreground',
                )}
              >
                <Icon size={16} />
              </button>
            );
          })}
        </div>
      </FormField>

      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy}>
          Save appearance
        </Button>
      </div>
    </div>
  );
}
