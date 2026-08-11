// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor for branding.default (ADR-038 Phase 2): the title, palette and logo
// every tenant inherits unless it sets its own.
//
// It shares the theme fields and the live preview with the tenant editor. The
// logo does NOT come along: a tenant can upload one to the object store, while
// this tier takes an https URL. That is not an omission to fix later — an
// instance default is served to every tenant, and an operator uploading a blob
// that every tenant then serves is a different feature with different storage.

import { Palette } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import {
  BrandingThemeFields,
  BrandingPreview,
  brandingBadHex,
  EMPTY_BRANDING_THEME,
  type BrandingThemeState,
} from '@/components/branding/BrandingThemeFields';
import { defineSetting, onlyKnownKeys, parseJson, type SettingEditorProps } from './registry';

/** The form state: the shared theme plus this tier's logo URL. */
interface BrandingDefaultForm extends BrandingThemeState {
  logo: string;
}

/**
 * Every key this editor models. Anything else — a case variant the server's
 * decoder binds, or a field a newer server grew — drops to the raw JSON editor
 * instead of being partially loaded and saved over.
 */
const KNOWN_KEYS = [
  'title',
  'logo',
  'logoMaxHeight',
  'primary',
  'background',
  'foreground',
  'accent',
] as const;

interface BrandingValue {
  title?: string | null;
  logo?: string | null;
  logoMaxHeight?: number | string | null;
  primary?: string | null;
  background?: string | null;
  foreground?: string | null;
  accent?: string | null;
}

function strText(v: unknown): string {
  return typeof v === 'string' ? v : '';
}

function seed(json: string): BrandingDefaultForm | null {
  const v = onlyKnownKeys<BrandingValue>(parseJson(json), KNOWN_KEYS);
  if (v === null) return null;
  return {
    ...EMPTY_BRANDING_THEME,
    title: strText(v.title),
    logo: strText(v.logo),
    logoMaxHeight:
      typeof v.logoMaxHeight === 'number' && Number.isFinite(v.logoMaxHeight)
        ? String(v.logoMaxHeight)
        : typeof v.logoMaxHeight === 'string'
          ? v.logoMaxHeight
          : '',
    primary: strText(v.primary),
    background: strText(v.background),
    foreground: strText(v.foreground),
    accent: strText(v.accent),
  };
}

// An empty field is OMITTED rather than written as null: the server decodes this
// with DisallowUnknownFields into a struct of pointers, and an absent key is how
// "inherit the console's built-in look" is expressed. Trimming and coercion are
// safe here because the result never returns to the editor.
function toJson(form: BrandingDefaultForm): string {
  const out: BrandingValue = {};
  for (const k of ['title', 'logo', 'primary', 'background', 'foreground', 'accent'] as const) {
    const raw = form[k].trim();
    if (raw !== '') out[k] = raw;
  }
  // Same rule as the basemap camera fields: toJson is TOTAL, so a half-typed
  // height is written through as text for validate to refuse. Omitting it would
  // drop it silently; coercing it would write NaN, which JSON.stringify renders
  // as null — a height cleared without being asked.
  const height = form.logoMaxHeight.trim();
  if (height !== '') out.logoMaxHeight = Number.isFinite(Number(height)) ? Number(height) : height;
  return JSON.stringify(out);
}

// Validates the produced JSON rather than the form, so what is checked is what
// would be sent.
function validate(json: string) {
  const parsed = seed(json);
  if (parsed === null) return { key: 'valueMustBeJsonError' };
  const bad = brandingBadHex(parsed);
  if (bad.length > 0) return { key: 'branding:badHexError', values: { fields: bad.join(', ') } };
  // Unreachable today from either direction — the field is <input type="number">
  // and the server decodes this into an *int, so a non-numeric height is filtered
  // on the way in and refused on the way out. Kept anyway, as the counterpart of
  // the basemap camera-field rule: the rule belongs beside its sibling rather
  // than resting on an input attribute nobody would think to preserve.
  const height = parsed.logoMaxHeight.trim();
  if (height !== '' && !Number.isFinite(Number(height))) {
    return { key: 'brandingIssueHeightNotANumber' };
  }
  return null;
}

function BrandingDefaultEditor({ value, onChange }: SettingEditorProps<BrandingDefaultForm>) {
  const { t } = useTranslation('branding');
  const setLogoField = (v: string) => onChange({ ...value, logo: v });

  return (
    <div className="grid gap-8 lg:grid-cols-[1fr_20rem]">
      <div className="space-y-6">
        <BrandingThemeFields value={value} onChange={(next) => onChange({ ...value, ...next })} />
        <FormField label={t('logoLabel')} htmlFor="bd-logo" description={t('logoDescription')}>
          <Input
            id="bd-logo"
            value={value.logo}
            placeholder={t('httpsPlaceholder')}
            onChange={(e) => setLogoField(e.target.value)}
          />
        </FormField>
      </div>
      {/* The preview shows the logo only when it is a directly-usable URL, which
          at this tier it always is — there is no object-store proxy path here. */}
      <BrandingPreview form={value} logoSrc={value.logo.trim() || null} />
    </div>
  );
}

export const brandingDefaultSection = defineSetting<BrandingDefaultForm>({
  key: 'branding.default',
  labelKey: 'tabBranding',
  icon: Palette,
  seed,
  toJson,
  validate,
  Editor: BrandingDefaultEditor,
});
