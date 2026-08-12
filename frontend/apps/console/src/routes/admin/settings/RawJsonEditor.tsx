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
 * The fallback section. ONE module-level object, not a per-key factory: a section
 * built during a render is a new component type every render, which remounts the
 * textarea and drops focus after every keystroke — which is what a per-key
 * factory called from the host's render body did. The host already knows the key,
 * and labelKey is null so the tab is labelled with it.
 */
export const RAW_JSON_SECTION: SettingSection = defineSetting<RawForm>({
  key: '',
  labelKey: null,
  icon: Braces,
  // Never null — this is the terminal fallback, so there is nothing further to
  // fall back TO. Invalid JSON is reported by validate, not by refusing to render.
  seed: (json) => ({ text: json }),
  toJson: (draft) => draft.text,
  // The degenerate case of the rule, and the clearest statement of it: this
  // editor's produced JSON is exactly what the operator typed, and the only thing
  // that can be wrong with it is that it does not parse.
  validate: (json) => {
    try {
      JSON.parse(json);
      return null;
    } catch {
      return { key: 'valueMustBeJsonError' };
    }
  },
  Editor: RawJsonEditor,
});
