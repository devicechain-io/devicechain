// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The branding THEME form — title, palette, logo height — plus its live preview,
// shared by the two tiers that set one (ADR-038):
//
//   the TENANT's own branding      BrandingPage
//   the INSTANCE default           the branding.default system setting
//
// The LOGO is deliberately NOT here. The two tiers handle it differently and for
// a real reason: a tenant can upload one to the object store (ADR-058), which
// needs immediate actions and a proxy path that cannot round-trip through a
// theme save, while the instance default takes an https URL and nothing else. A
// shared control would have to grow a mode flag to express that, and would make
// the object-store path look available at a tier where it is not.

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { X } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form-field';
import { ColorPicker } from '@/components/ui/color-picker';
import { contrastRatio } from '@/lib/branding';

export const HEX_RE = /^#[0-9a-fA-F]{6}$/;

/** The theme form's per-field state — strings so "" cleanly represents "inherit". */
export interface BrandingThemeState {
  title: string;
  logoMaxHeight: string;
  primary: string;
  background: string;
  foreground: string;
  accent: string;
}

export const EMPTY_BRANDING_THEME: BrandingThemeState = {
  title: '',
  logoMaxHeight: '',
  primary: '',
  background: '',
  foreground: '',
  accent: '',
};

/** The color fields that are set but not six-digit hex. Client pre-check only —
 *  the server re-validates and is the authority. */
export function brandingBadHex(form: BrandingThemeState): string[] {
  return (['primary', 'background', 'foreground', 'accent'] as const).filter(
    (k) => form[k] !== '' && !HEX_RE.test(form[k]),
  );
}

export function BrandingThemeFields({
  value,
  onChange,
}: {
  value: BrandingThemeState;
  onChange: (next: BrandingThemeState) => void;
}) {
  const { t } = useTranslation('branding');
  const set = (patch: Partial<BrandingThemeState>) => onChange({ ...value, ...patch });
  // Bound per-field setters — each closes over a literal key OUTSIDE any JSX
  // attribute, so the key is a plain identifier by the time it reaches JSX (a
  // JSX-attribute call argument here would trip the i18n literal-string lint,
  // even though these keys are technical, never user-facing text).
  const setTitleField = (v: string) => set({ title: v });
  const setPrimaryField = (v: string) => set({ primary: v });
  const setAccentField = (v: string) => set({ accent: v });
  const setBackgroundField = (v: string) => set({ background: v });
  const setForegroundField = (v: string) => set({ foreground: v });
  const setLogoMaxHeightField = (v: string) => set({ logoMaxHeight: v });

  // Non-blocking contrast hints (guidance only, never block a save — ADR-038 §4).
  const contrast = useMemo(() => {
    if (!HEX_RE.test(value.foreground) || !HEX_RE.test(value.background)) return null;
    return contrastRatio(value.foreground, value.background);
  }, [value.foreground, value.background]);
  const primaryContrast = useMemo(() => {
    if (!HEX_RE.test(value.primary)) return null;
    return contrastRatio(value.primary, '#ffffff');
  }, [value.primary]);

  return (
    <>
      <FormField label={t('appTitleLabel')} htmlFor="b-title" description={t('appTitleDescription')}>
        <Input
          id="b-title"
          value={value.title}
          maxLength={64}
          placeholder="DeviceChain"
          onChange={(e) => setTitleField(e.target.value)}
        />
      </FormField>

      <div className="grid gap-4 sm:grid-cols-2">
        <ColorField label={t('primaryLabel')} hint={t('primaryHint')} value={value.primary} onChange={setPrimaryField} />
        <ColorField label={t('accentLabel')} hint={t('accentHint')} value={value.accent} onChange={setAccentField} />
        <ColorField label={t('sidebarBackgroundLabel')} hint={t('sidebarBackgroundHint')} value={value.background} onChange={setBackgroundField} />
        <ColorField label={t('sidebarForegroundLabel')} hint={t('sidebarForegroundHint')} value={value.foreground} onChange={setForegroundField} />
      </div>

      {contrast !== null && contrast < 4.5 && (
        <p className="text-label-lg text-warning">{t('contrastWarning', { ratio: contrast.toFixed(1) })}</p>
      )}
      {primaryContrast !== null && primaryContrast < 4.5 && (
        <p className="text-label-lg text-warning">
          {t('primaryContrastWarning', { ratio: primaryContrast.toFixed(1) })}
        </p>
      )}

      <FormField label={t('logoMaxHeightLabel')} htmlFor="b-height" description={t('logoMaxHeightDescription')}>
        <Input
          id="b-height"
          type="number"
          min={16}
          max={200}
          value={value.logoMaxHeight}
          placeholder="28"
          onChange={(e) => setLogoMaxHeightField(e.target.value)}
          className="w-32"
        />
      </FormField>
    </>
  );
}

// A color field: a swatch picker + a hex text input, with a Clear that re-inherits.
function ColorField({
  label,
  hint,
  value,
  onChange,
}: {
  label: string;
  hint: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const { t } = useTranslation('branding');
  const valid = HEX_RE.test(value);
  return (
    <FormField label={label} description={hint}>
      <div className="flex items-center gap-2">
        <ColorPicker
          ariaLabel={t('colorSwatchAriaLabel', { label })}
          value={valid ? value : '#000000'}
          onChange={onChange}
        />
        <Input
          value={value}
          placeholder={t('inheritPlaceholder')}
          onChange={(e) => onChange(e.target.value)}
          className="font-mono"
        />
        {value !== '' && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={t('clearColorLabel', { label })}
            onClick={() => onChange('')}
          >
            <X className="size-3.5" />
          </Button>
        )}
      </div>
    </FormField>
  );
}

// A live preview of the palette + logo. Colors come from the (unsaved) theme form.
export function BrandingPreview({
  form,
  logoSrc,
}: {
  form: BrandingThemeState;
  logoSrc: string | null;
}) {
  const { t } = useTranslation('branding');
  const primary = HEX_RE.test(form.primary) ? form.primary : undefined;
  const bg = HEX_RE.test(form.background) ? form.background : undefined;
  const fg = HEX_RE.test(form.foreground) ? form.foreground : undefined;
  const height = form.logoMaxHeight.trim() === '' ? 28 : Number(form.logoMaxHeight);
  return (
    <div className="space-y-2">
      <p className="text-sm font-medium text-foreground">{t('preview')}</p>
      <div className="overflow-hidden rounded-lg border border-border">
        <div className="flex items-center gap-2 px-3 py-3" style={{ background: bg, color: fg }}>
          {logoSrc ? (
            <img src={logoSrc} alt="" className="w-auto max-w-[70%] object-contain" style={{ maxHeight: height }} />
          ) : (
            <span className="text-sm font-semibold">{form.title.trim() || 'DeviceChain'}</span>
          )}
        </div>
        <div className="space-y-3 bg-card p-3">
          {/* A PICTURE of a button, not a button: it shows what this tier's primary
              colour does to one, so it must carry that colour rather than the kit's
              — <Button> would render the console's own styling and preview nothing.
              Rendering it as a <span> also stops it being a tab stop that does
              nothing when activated, which is what it was. */}
          <span
            className="inline-block rounded-md px-3 py-1.5 text-sm font-medium text-white"
            style={{ background: primary ?? 'hsl(var(--primary))' }}
          >
            {t('primaryButtonSample')}
          </span>
          <p className="text-xs text-muted-foreground">{t('sampleContent')}</p>
        </div>
      </div>
    </div>
  );
}
