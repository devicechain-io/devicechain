// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The JSON editor every setting falls back to. It exists for two situations, and
// both are cases where hiding the value would be worse than showing raw JSON:
//
//   - a key the SERVER knows and this console build has no editor for. The
//     settings vocabulary lives in the backend, so a newer instance can offer a
//     key an older console has never heard of. Showing it raw keeps it editable;
//     omitting it would make a configured setting invisible.
//   - a stored value a section's own editor cannot read. That is either a value
//     written before the shape changed, or one written through the API — either
//     way the operator needs to SEE it to fix it, and a typed form rendered over
//     an unreadable value would show empty fields above real data and then save
//     the emptiness.
//
// It is not a lesser path to be embarrassed about: the server validates every
// write regardless of which editor produced it, so raw JSON is exactly as safe as
// a form. What it lacks is guidance, not safety.

import { useTranslation } from 'react-i18next';
import { Braces } from 'lucide-react';
import { Textarea } from '@/components/ui/textarea';
import { defineSetting, type SettingEditorProps, type SettingSection } from './registry';

interface RawForm {
  text: string;
}

function RawJsonEditor({ value, onChange }: SettingEditorProps<RawForm>) {
  const { t } = useTranslation('adminSettings');
  return (
    <Textarea
      value={value.text}
      spellCheck={false}
      aria-label={t('rawJsonLabel')}
      className="min-h-48"
      onChange={(e) => onChange({ text: e.target.value })}
    />
  );
}

/**
 * rawJsonSection builds the fallback section for a key. labelKey is null: the tab
 * is labelled with the setting key itself, because a key with no editor has no
 * name of ours to show.
 */
export function rawJsonSection(key: string): SettingSection {
  return defineSetting<RawForm>({
    key,
    labelKey: null,
    icon: Braces,
    // Never null — this is the terminal fallback, so there is nothing further to
    // fall back TO. Invalid JSON is reported by validate, not by refusing to render.
    parse: (json) => ({ text: json }),
    serialize: (draft) => draft.text,
    validate: (draft) => {
      try {
        JSON.parse(draft.text);
        return null;
      } catch {
        return { key: 'valueMustBeJsonError' };
      }
    },
    Editor: RawJsonEditor,
  });
}
