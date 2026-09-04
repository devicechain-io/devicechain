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
import { DEFAULT_LOCALE, SUPPORTED_LOCALES, isShippedLocale } from '@/i18n/config';
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
 * The canonical form the server stores: trimmed, language lowercased, a 4-letter
 * script Titlecased, everything else uppercased. Mirrors locale.Normalize in Go.
 *
 * 🔴 The editor emits this rather than what was typed, because the server REFUSES a
 * non-canonical tag — it holds a Validator and no normalizer, so anything it accepts
 * is stored verbatim, and a stored `"  ES-mx "` would come back and make this setting
 * read as DIRTY on load (the host compares the serialized draft against the stored
 * bytes) with Save enabled before anyone typed. Canonicalizing here keeps that refusal
 * invisible in the UI: it can only be reached by a raw caller, who is told the form to
 * send.
 */
function canonicalize(tag: string): string {
  return tag
    .trim()
    .split('-')
    .map((part, i) => {
      if (i === 0) return part.toLowerCase();
      if (part.length === 4) return part[0].toUpperCase() + part.slice(1).toLowerCase();
      return part.toUpperCase();
    })
    .join('-');
}

/**
 * Stored JSON → the form state, which is the tag as text — with an EMPTY string
 * standing for the setting's `null`.
 *
 * 🔴 `null` IS A REAL STATE HERE, not a missing value: it is the shipped default and
 * it means "no instance default — each viewer's browser decides". Dropping it to the
 * raw JSON editor (which is what returning null here would do) would leave the one
 * value an operator most needs to be able to choose reachable only by typing `null`
 * into a textarea.
 *
 * Anything that is neither a string nor null — an object, a number, an unparseable
 * value — still falls back, because this editor genuinely cannot model it.
 */
function seed(json: string): string | null {
  const v = parseJson(json);
  if (v === null) return '';
  return typeof v === 'string' ? v : null;
}

// TOTAL, as the contract requires: whatever the operator has typed is written through,
// valid or not, so validate can see it and refuse it. An empty field is the `null`
// state rather than a blank tag — the server refuses a blank precisely so the two
// cannot be confused.
function toJson(tag: string): string {
  return tag.trim() === '' ? 'null' : JSON.stringify(canonicalize(tag));
}

// Validates the produced JSON rather than the draft, so the thing checked IS the thing
// sent.
function validate(json: string): SettingIssue | null {
  const v = parseJson(json);
  if (v === null) return null; // "the browser decides" — the shipped default, and legal
  if (typeof v !== 'string') return { key: 'valueMustBeJsonError' };
  const tag = v.trim();
  // Unreachable from this editor (an empty field serializes to null above), but a value
  // typed into the raw editor or written by a script can hold one, and the server
  // refuses it: a blank is a MISSING tag, not a second spelling of null.
  if (tag === '') return { key: 'localeIssueRequired' };
  if (tag.length > MAX_LEN) return { key: 'localeIssueTooLong', values: { max: MAX_LEN } };
  if (!TAG.test(tag)) return { key: 'localeIssueNotATag', values: { tag } };
  return null;
}

function LocaleDefaultEditor({ value, onChange }: SettingEditorProps<string>) {
  const { t } = useTranslation('adminSettings');
  const tag = value.trim();
  // 🔴 One question, one answer: isShippedLocale is i18next's own resolution test, the
  // same one applyTenantDefaultLocale gates on. This file used to carry a second,
  // hand-rolled copy that disagreed with i18next on case — so the warning below could
  // stay silent for a tag that renders nothing.
  const isShipped = tag !== '' && isShippedLocale(canonicalize(tag));

  return (
    <div className="max-w-xl space-y-3">
      <FormField
        label={t('localeTagLabel')}
        htmlFor="locale-default-tag"
        description={t('localeTagHelp')}
      >
        {/* The placeholder is the SHIPPED FALLBACK language rather than a hard-coded
            example. It is a language TAG — an identifier like a token, never
            localized. Note it is NOT the shipped default of this setting, which is
            "no default at all"; an empty field is that state. */}
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

      {/* 🔴 An empty field is a CHOICE, so it says what that choice does. Left unlabelled
          it reads as an unfinished form, which is how the shipped default came to be a
          concrete "en" in the first place — and that silently outranked every viewer's
          browser on every instance nobody had configured. */}
      {tag === '' && <HintText size="md">{t('localeNoDefaultHint')}</HintText>}

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
