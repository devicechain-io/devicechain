// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Self-service tenant white-labeling editor (ADR-038 Phase 2 / ADR-058). A tenant
// admin (branding:write) edits their OWN tenant's title, palette, and logo. Title,
// colors, and logo height are the THEME — edited as the RAW override
// (tenant.brandingOverride, an empty field = inherit) and committed together on
// Save. The LOGO is managed separately with immediate actions (upload to the object
// store, set an https URL, or remove): a client cannot round-trip an object-store
// logo reference through the theme's full replace, so keeping it on the theme save
// would wipe an uploaded logo. Each write returns the freshly-resolved tenant, which
// we write straight into the tenant cache so the rebrand shows across the shell.

import { useEffect, useMemo, useRef, useState } from 'react';
import { Upload, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import { useCurrentTenant, useSetCurrentTenant } from '@/auth/TenantProvider';
import {
  getCurrentTenant,
  setTenantBranding,
  setTenantLogo,
  uploadTenantLogo,
  type TenantBranding,
  type TenantBrandingInput,
} from '@/lib/api/user-management';
import { contrastRatio } from '@/lib/branding';
import { useBrandingLogoSrc } from '@/lib/useBrandingLogo';
import { errMessage } from '@/routes/common';

// Mirrors the server allow-list (branding package): uploaded logos are raster-only
// (SVG must be an https URL) and capped on bytes. Client pre-check only — fail-fast
// UX; the server re-validates (and sniffs the real type).
const UPLOAD_LOGO_MIME = ['image/png', 'image/jpeg', 'image/webp'];
const MAX_UPLOAD_LOGO_BYTES = 1024 * 1024; // 1 MiB (branding.MaxUploadedLogoBytes)
const HEX_RE = /^#[0-9a-fA-F]{6}$/;
// An object-store logo surfaces to the client as this proxy path, not a URL — the
// URL field is left blank for one (it is an upload, not a pasted link).
const PROXY_LOGO_PREFIX = '/branding/logo';

// The authority name shown in the permission-denied message, in a font-mono span —
// a technical identifier, never localized (mirrors how a token/id is displayed).
const BRANDING_WRITE_AUTHORITY = 'branding:write';

// The theme form's per-field state — strings so "" cleanly represents "inherit".
interface FormState {
  title: string;
  logoMaxHeight: string;
  primary: string;
  background: string;
  foreground: string;
  accent: string;
}

function initialState(o: TenantBranding | null): FormState {
  return {
    title: o?.title ?? '',
    logoMaxHeight: o?.logoMaxHeight != null ? String(o.logoMaxHeight) : '',
    primary: o?.primary ?? '',
    background: o?.background ?? '',
    foreground: o?.foreground ?? '',
    accent: o?.accent ?? '',
  };
}

export default function BrandingPage() {
  const { t } = useTranslation('branding');
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'branding:write');
  const tenant = useCurrentTenant();

  if (!canWrite) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <p className="text-sm text-muted-foreground">
          {t('noPermissionPrefix')}
          <span className="font-mono"> {BRANDING_WRITE_AUTHORITY} </span>
          {t('noPermissionSuffix')}
        </p>
      </PageShell>
    );
  }

  // Seed the editor only from a REAL fetched override. A fetched tenant always
  // carries a non-null brandingOverride (GraphQL `TenantBranding!`), so a null here
  // means the tenant fetch hasn't landed yet — gate on a loading state rather than
  // seed the form blank, which would make a theme save full-replace-clear every
  // existing override (setTenantBranding is a full replace of the theme; a blank
  // field = clear). Keyed by token only: a logo action refetches the tenant, and
  // remounting on that would discard the user's in-progress theme edits.
  const override = tenant?.brandingOverride ?? null;
  if (!override) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <LoadingState description={t('loadingBranding')} />
      </PageShell>
    );
  }
  return <BrandingEditor key={tenant?.token} override={override} />;
}

function BrandingEditor({ override }: { override: TenantBranding }) {
  const { t } = useTranslation('branding');
  const applyTenant = useSetCurrentTenant();
  const tenant = useCurrentTenant();
  const toast = useToast();

  const [form, setForm] = useState<FormState>(() => initialState(override));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [logoBusy, setLogoBusy] = useState(false);
  // Tracks whether the user has edited the theme form since it was last seeded, so a
  // background refetch (stale-while-revalidate cache) can re-seed a PRISTINE form
  // without clobbering in-progress edits. The editor is keyed by token only (so a
  // logo action does not remount and discard edits), which drops the remount-reseed;
  // this restores the stale-seed protection without the remount.
  const [dirty, setDirty] = useState(false);
  // The https/data URL field. Seeded from the current override logo only when it is
  // a directly-usable value (not an object-store proxy path).
  const [logoUrl, setLogoUrl] = useState(() => {
    const l = override.logo ?? '';
    return l.startsWith(PROXY_LOGO_PREFIX) ? '' : l;
  });

  // Re-seed a pristine theme form when a newer override arrives (e.g. the initial
  // seed came from a stale cached tenant, or another admin changed the theme). Never
  // re-seed once the user has started editing — their edits win until they Save.
  useEffect(() => {
    if (!dirty) setForm(initialState(override));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [override.updatedAt]);

  const set = (k: keyof FormState, v: string) => {
    setDirty(true);
    setForm((f) => ({ ...f, [k]: v }));
  };
  // Bound per-field setters — each closes over a literal FormState key OUTSIDE any
  // JSX attribute, so the key itself is a plain identifier by the time it reaches
  // JSX (a JSX-attribute call argument here would trip the i18n literal-string lint,
  // even though these keys are technical, never user-facing text).
  const setTitleField = (v: string) => set('title', v);
  const setPrimaryField = (v: string) => set('primary', v);
  const setAccentField = (v: string) => set('accent', v);
  const setBackgroundField = (v: string) => set('background', v);
  const setForegroundField = (v: string) => set('foreground', v);
  const setLogoMaxHeightField = (v: string) => set('logoMaxHeight', v);

  // The live resolved logo (updated by the logo actions via the tenant cache), used
  // for the preview. Whether THIS tenant set its own logo — which gates the Remove
  // action — is the raw override, not the resolved cascade: an inherited operator-
  // default logo is not this tenant's to remove.
  const logoPreviewSrc = useBrandingLogoSrc(tenant?.branding?.logo);
  const hasOwnLogo = !!tenant?.brandingOverride?.logo;

  // Client-side hex validation (fail-fast); the server re-validates.
  const badHex = (['primary', 'background', 'foreground', 'accent'] as const).filter(
    (k) => form[k] !== '' && !HEX_RE.test(form[k]),
  );

  // Non-blocking contrast hints (guidance only, never block a save — ADR-038 §4).
  const contrast = useMemo(() => {
    if (!HEX_RE.test(form.foreground) || !HEX_RE.test(form.background)) return null;
    return contrastRatio(form.foreground, form.background);
  }, [form.foreground, form.background]);
  const primaryContrast = useMemo(() => {
    if (!HEX_RE.test(form.primary)) return null;
    return contrastRatio(form.primary, '#ffffff');
  }, [form.primary]);

  const submit = async () => {
    setFormError(null);
    if (badHex.length > 0) {
      setFormError(t('badHexError', { fields: badHex.join(', ') }));
      return;
    }
    const height = form.logoMaxHeight.trim();
    const input: TenantBrandingInput = {
      title: emptyToNull(form.title.trim()),
      logoMaxHeight: height === '' ? null : Number(height),
      primary: emptyToNull(form.primary.trim()),
      background: emptyToNull(form.background.trim()),
      foreground: emptyToNull(form.foreground.trim()),
      accent: emptyToNull(form.accent.trim()),
    };
    setBusy(true);
    try {
      const updated = await setTenantBranding(input);
      applyTenant(updated); // write-through cache → shell re-themes immediately
      setForm(initialState(updated.brandingOverride));
      setDirty(false);
      toast.toast(t('brandingSaved'));
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  // ── Logo actions (immediate; each refreshes the tenant cache) ──────────────

  const onUpload = async (file: File) => {
    setFormError(null);
    if (!UPLOAD_LOGO_MIME.includes(file.type)) {
      setFormError(t('uploadedLogoMimeError'));
      return;
    }
    if (file.size > MAX_UPLOAD_LOGO_BYTES) {
      setFormError(t('logoTooLarge', { sizeKb: Math.round(file.size / 1024) }));
      return;
    }
    setLogoBusy(true);
    try {
      await uploadTenantLogo(file);
    } catch (err) {
      setFormError(errMessage(err)); // the upload itself failed — nothing persisted
      setLogoBusy(false);
      return;
    }
    // The upload persisted the reference; refetch to pick up the resolved branding.
    // A refetch failure here does NOT mean the upload failed, so report success and
    // let the next natural load reconcile rather than implying the upload was lost.
    try {
      applyTenant(await getCurrentTenant());
    } catch {
      /* logo is persisted; the shell will pick it up on the next refresh */
    }
    setLogoUrl('');
    toast.toast(t('logoUploaded'));
    setLogoBusy(false);
  };

  const applyLogoUrl = async () => {
    setFormError(null);
    // Blank is NOT a clear here — removal is the explicit "Remove logo" action, so an
    // accidental "Apply URL" with an empty field can't silently delete an uploaded
    // logo (its blob is GC'd server-side and unrecoverable).
    if (logoUrl.trim() === '') {
      setFormError(t('logoUrlRequired'));
      return;
    }
    setLogoBusy(true);
    try {
      applyTenant(await setTenantLogo(logoUrl.trim()));
      toast.toast(t('logoUpdated'));
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setLogoBusy(false);
    }
  };

  const removeLogo = async () => {
    setFormError(null);
    setLogoBusy(true);
    try {
      applyTenant(await setTenantLogo(null));
      setLogoUrl('');
      toast.toast(t('logoRemoved'));
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setLogoBusy(false);
    }
  };

  return (
    <PageShell
      title={t('title')}
      description={t('editorDescription')}
      action={
        <Button onClick={submit} loading={busy} disabled={busy}>
          {t('saveBranding')}
        </Button>
      }
    >
      <div className="mx-auto grid max-w-5xl gap-8 lg:grid-cols-[1fr_20rem]">
        <div className="space-y-6">
          {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}

          <FormField label={t('appTitleLabel')} htmlFor="b-title" description={t('appTitleDescription')}>
            <Input id="b-title" value={form.title} maxLength={64} placeholder="DeviceChain" onChange={(e) => setTitleField(e.target.value)} />
          </FormField>

          <div className="grid gap-4 sm:grid-cols-2">
            <ColorField label={t('primaryLabel')} hint={t('primaryHint')} value={form.primary} onChange={setPrimaryField} />
            <ColorField label={t('accentLabel')} hint={t('accentHint')} value={form.accent} onChange={setAccentField} />
            <ColorField label={t('sidebarBackgroundLabel')} hint={t('sidebarBackgroundHint')} value={form.background} onChange={setBackgroundField} />
            <ColorField label={t('sidebarForegroundLabel')} hint={t('sidebarForegroundHint')} value={form.foreground} onChange={setForegroundField} />
          </div>

          {contrast !== null && contrast < 4.5 && (
            <p className="text-label-lg text-warning">
              {t('contrastWarning', { ratio: contrast.toFixed(1) })}
            </p>
          )}
          {primaryContrast !== null && primaryContrast < 4.5 && (
            <p className="text-label-lg text-warning">
              {t('primaryContrastWarning', { ratio: primaryContrast.toFixed(1) })}
            </p>
          )}

          <FormField
            label={t('logoLabel')}
            description={t('logoDescription')}
          >
            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <Input
                  aria-label={t('logoUrlAriaLabel')}
                  value={logoUrl}
                  placeholder={t('httpsPlaceholder')}
                  disabled={logoBusy}
                  onChange={(e) => setLogoUrl(e.target.value)}
                />
                <Button type="button" variant="outline" size="sm" disabled={logoBusy} onClick={applyLogoUrl}>
                  {t('applyUrl')}
                </Button>
              </div>
              <div className="flex items-center gap-2">
                <UploadButton disabled={logoBusy} onPick={onUpload} />
                {hasOwnLogo && (
                  <Button type="button" variant="ghost" size="sm" disabled={logoBusy} onClick={removeLogo}>
                    <X className="mr-1 size-3.5" /> {t('removeLogo')}
                  </Button>
                )}
              </div>
            </div>
          </FormField>

          <FormField label={t('logoMaxHeightLabel')} htmlFor="b-height" description={t('logoMaxHeightDescription')}>
            <Input id="b-height" type="number" min={16} max={200} value={form.logoMaxHeight} placeholder="28" onChange={(e) => setLogoMaxHeightField(e.target.value)} className="w-32" />
          </FormField>
        </div>

        <BrandingPreview form={form} logoSrc={logoPreviewSrc} />
      </div>
    </PageShell>
  );
}

// A color field: a native swatch + a hex text input, with a Clear that re-inherits.
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
        <input
          type="color"
          aria-label={t('colorSwatchAriaLabel', { label })}
          value={valid ? value : '#000000'}
          onChange={(e) => onChange(e.target.value)}
          className="size-9 shrink-0 cursor-pointer rounded border border-border bg-transparent p-0.5"
        />
        <Input value={value} placeholder={t('inheritPlaceholder')} onChange={(e) => onChange(e.target.value)} className="font-mono" />
        {value !== '' && (
          <Button type="button" variant="ghost" size="icon" aria-label={t('clearColorLabel', { label })} onClick={() => onChange('')}>
            <X className="size-3.5" />
          </Button>
        )}
      </div>
    </FormField>
  );
}

function UploadButton({ onPick, disabled }: { onPick: (file: File) => void; disabled?: boolean }) {
  const { t } = useTranslation('branding');
  const ref = useRef<HTMLInputElement>(null);
  return (
    <>
      <input
        ref={ref}
        type="file"
        accept="image/png,image/jpeg,image/webp"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) onPick(file);
          e.target.value = ''; // allow re-picking the same file
        }}
      />
      <Button type="button" variant="outline" size="sm" disabled={disabled} onClick={() => ref.current?.click()}>
        <Upload className="mr-1 size-3.5" /> {t('upload')}
      </Button>
    </>
  );
}

// A live preview of the palette + logo. Colors come from the (unsaved) theme form;
// the logo is the live resolved logo (already applied), shown only in an <img>.
function BrandingPreview({ form, logoSrc }: { form: FormState; logoSrc: string | null }) {
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
          <button
            type="button"
            className="rounded-md px-3 py-1.5 text-sm font-medium text-white"
            style={{ background: primary ?? 'hsl(var(--primary))' }}
          >
            {t('primaryButtonSample')}
          </button>
          <p className="text-xs text-muted-foreground">{t('sampleContent')}</p>
        </div>
      </div>
    </div>
  );
}

function emptyToNull(v: string): string | null {
  return v === '' ? null : v;
}
