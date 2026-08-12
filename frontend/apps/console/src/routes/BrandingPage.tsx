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

import { useEffect, useState } from 'react';
import { Upload, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { useToast } from '@/components/ui/toast';
import { FilePicker } from '@/components/ui/file-picker';
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
import {
  BrandingThemeFields,
  BrandingPreview,
  brandingBadHex,
  type BrandingThemeState,
} from '@/components/branding/BrandingThemeFields';
import { useBrandingLogoSrc } from '@/lib/useBrandingLogo';
import { errMessage } from '@/routes/common';

// Mirrors the server allow-list (branding package): uploaded logos are raster-only
// (SVG must be an https URL) and capped on bytes. Client pre-check only — fail-fast
// UX; the server re-validates (and sniffs the real type).
const UPLOAD_LOGO_MIME = ['image/png', 'image/jpeg', 'image/webp'];
const MAX_UPLOAD_LOGO_BYTES = 1024 * 1024; // 1 MiB (branding.MaxUploadedLogoBytes)
// An object-store logo surfaces to the client as this proxy path, not a URL — the
// URL field is left blank for one (it is an upload, not a pasted link).
const PROXY_LOGO_PREFIX = '/branding/logo';

// The authority name shown in the permission-denied message, in a font-mono span —
// a technical identifier, never localized (mirrors how a token/id is displayed).
const BRANDING_WRITE_AUTHORITY = 'branding:write';

function initialState(o: TenantBranding | null): BrandingThemeState {
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

  const [form, setForm] = useState<BrandingThemeState>(() => initialState(override));
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

  const change = (next: BrandingThemeState) => {
    setDirty(true);
    setForm(next);
  };

  // The live resolved logo (updated by the logo actions via the tenant cache), used
  // for the preview. Whether THIS tenant set its own logo — which gates the Remove
  // action — is the raw override, not the resolved cascade: an inherited operator-
  // default logo is not this tenant's to remove.
  const logoPreviewSrc = useBrandingLogoSrc(tenant?.branding?.logo);
  const hasOwnLogo = !!tenant?.brandingOverride?.logo;

  const badHex = brandingBadHex(form);

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

          <BrandingThemeFields value={form} onChange={change} />

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

        </div>

        <BrandingPreview form={form} logoSrc={logoPreviewSrc} />
      </div>
    </PageShell>
  );
}

function UploadButton({ onPick, disabled }: { onPick: (file: File) => void; disabled?: boolean }) {
  const { t } = useTranslation('branding');
  return (
    <>
      <FilePicker accept="image/png,image/jpeg,image/webp" disabled={disabled} onPick={onPick}>
        <Upload className="mr-1 size-3.5" /> {t('upload')}
      </FilePicker>
    </>
  );
}

function emptyToNull(v: string): string | null {
  return v === '' ? null : v;
}
