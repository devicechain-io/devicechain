// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor for locale.default: the console language every tenant inherits unless
// it sets its own, and every user inherits unless they pick one from the switcher.
//
// 🔴 Unlike the other three settings this value is a BARE JSON STRING, not an
// object — a locale is one scalar, and wrapping it would buy a field name nobody
// reads. That is why nothing here uses onlyKnownKeys: there are no keys.
//
// 🔴 IT IS A FREE-TEXT TAG RATHER THAN A PICKER OVER THE SHIPPED CATALOGS, and that
// is the registry's "client validation must stay NO STRICTER than the server"
// rule doing its job rather than an unfinished control. The server deliberately
// accepts any well-formed language tag — the shipped set lives with the catalogs in
// the console, so mirroring it into the backend would fail in the wrong direction —
// and a picker here would refuse a tag the platform accepts while leaving the
// operator no way through, since a valid string never falls back to the raw editor.
// The shipped codes are offered as one-click shortcuts beside the field instead, so
// the common case costs a click and the uncommon one is still reachable.

import { Languages } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { Button } from '@/components/ui/button';
import { DEFAULT_LOCALE, SUPPORTED_LOCALES } from '@/i18n/config';
import { defineSetting, parseJson, type SettingEditorProps, type SettingIssue } from './registry';

// The tag grammar the server enforces, and the same subset: a 2- or 3-letter
// primary subtag, an optional 4-letter script, an optional 2-letter or 3-digit
// region. Kept identical rather than looser so an operator sees the reason here
// instead of after a round trip — and never STRICTER, which would refuse a value
// the platform accepts.
const TAG = /^[A-Za-z]{2,3}(-[A-Za-z]{4})?(-([A-Za-z]{2}|[0-9]{3}))?$/;

// Mirrors locale.MaxLen on the server.
const MAX_LEN = 35;

/**
 * Stored JSON → the form state, which is just the tag as text.
 *
 * Returns null for anything that is not a JSON string — an object, a number, or
 * `null` — which drops the setting to the raw editor rather than loading part of
 * it. `null` in particular is a value the server refuses but that an override
 * stored before that gate existed could still hold; showing it as raw JSON is the
 * honest answer, and the operator can repair or reset it there.
 */
function seed(json: string): string | null {
  const v = parseJson(json);
  return typeof v === 'string' ? v : null;
}

// TOTAL, as the contract requires: whatever the operator has typed is written
// through, valid or not, so validate can see it and refuse it. Trims freely —
// safe because the result never returns to the editor.
function toJson(tag: string): string {
  return JSON.stringify(tag.trim());
}

// Validates the produced JSON rather than the draft, so the thing checked IS the
// thing sent.
function validate(json: string): SettingIssue | null {
  const v = parseJson(json);
  if (typeof v !== 'string') return { key: 'valueMustBeJsonError' };
  const tag = v.trim();
  // Empty is refused rather than treated as "no default". Clearing the setting is
  // how an operator reverts to the shipped default, and admitting a second
  // spelling would give this page two states that look identical and behave
  // differently on Reset — the same rule the server applies.
  if (tag === '') return { key: 'localeIssueRequired' };
  if (tag.length > MAX_LEN) return { key: 'localeIssueTooLong', values: { max: MAX_LEN } };
  if (!TAG.test(tag)) return { key: 'localeIssueNotATag', values: { tag } };
  return null;
}

function LocaleDefaultEditor({ value, onChange }: SettingEditorProps<string>) {
  const { t } = useTranslation('adminSettings');
  // Matches applyTenantDefaultLocale's own resolvability test: a regional tag folds
  // onto its base catalog through i18next's nonExplicitSupportedLngs, so `es-MX`
  // renders Spanish and must NOT be warned about as though it rendered nothing.
  const tag = value.trim();
  const base = tag.split('-')[0].toLowerCase();
  const isShipped = SUPPORTED_LOCALES.some((l) => l.code === tag || l.code.toLowerCase() === base);

  return (
    <div className="max-w-xl space-y-3">
      <FormField
        label={t('localeTagLabel')}
        htmlFor="locale-default-tag"
        description={t('localeTagHelp')}
      >
        {/* The placeholder is the SHIPPED DEFAULT rather than a hard-coded example,
            so it cannot drift away from what an unset instance actually means. It is
            a language TAG — an identifier like a token, never localized. */}
        <Input
          id="locale-default-tag"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={DEFAULT_LOCALE}
          spellCheck={false}
        />
      </FormField>

      <div className="flex flex-wrap items-center gap-2">
        <HintText size="md">{t('localeShippedLabel')}</HintText>
        {SUPPORTED_LOCALES.map(({ code, label }) => (
          <Button
            key={code}
            type="button"
            variant="outline"
            size="sm"
            onClick={() => onChange(code)}
          >
            {/* The endonym is the language's own name and is never translated; the
                code beside it is the value that actually gets stored. */}
            <span className="font-mono">{code}</span>
            <span className="ms-1 text-muted-foreground">{label}</span>
          </Button>
        ))}
      </div>

      {/* 🔴 A WARNING, NOT AN ERROR — the save stays enabled. A tag with no catalog
          in this build is legal and inert until the catalog ships, so refusing it
          would be stricter than the server; saying nothing would leave an operator
          who typed "fr" wondering why nothing changed. */}
      {tag !== '' && !isShipped && (
        <HintText size="md">{t('localeNotShippedWarning', { tag })}</HintText>
      )}
    </div>
  );
}

export const localeDefaultSection = defineSetting<string>({
  key: 'locale.default',
  labelKey: 'tabLocale',
  icon: Languages,
  seed,
  toJson,
  validate,
  Editor: LocaleDefaultEditor,
});
