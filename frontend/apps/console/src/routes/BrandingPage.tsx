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
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'branding:write');
  const tenant = useCurrentTenant();

  if (!canWrite) {
    return (
      <PageShell title="Branding" description="Tenant white-labeling">
        <p className="text-sm text-muted-foreground">
          You don’t have permission to edit this tenant’s branding. Ask a tenant administrator for the
          <span className="font-mono"> branding:write </span> authority.
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
      <PageShell title="Branding" description="Tenant white-labeling">
        <LoadingState description="Loading branding…" />
      </PageShell>
    );
  }
  return <BrandingEditor key={tenant?.token} override={override} />;
}

function BrandingEditor({ override }: { override: TenantBranding }) {
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
      setFormError(`Enter a 6-digit hex color (like #1f9fb7) for: ${badHex.join(', ')}`);
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
      toast.toast('Branding saved');
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
      setFormError('Uploaded logos must be PNG, JPEG, or WebP. For SVG, host it and paste an https URL.');
      return;
    }
    if (file.size > MAX_UPLOAD_LOGO_BYTES) {
      setFormError(
        `Logo must be at most 1 MB (this file is ${Math.round(file.size / 1024)} KB).`,
      );
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
    toast.toast('Logo uploaded');
    setLogoBusy(false);
  };

  const applyLogoUrl = async () => {
    setFormError(null);
    // Blank is NOT a clear here — removal is the explicit "Remove logo" action, so an
    // accidental "Apply URL" with an empty field can't silently delete an uploaded
    // logo (its blob is GC'd server-side and unrecoverable).
    if (logoUrl.trim() === '') {
      setFormError('Enter an https URL, or use “Remove logo” to clear it.');
      return;
    }
    setLogoBusy(true);
    try {
      applyTenant(await setTenantLogo(logoUrl.trim()));
      toast.toast('Logo updated');
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
      toast.toast('Logo removed');
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setLogoBusy(false);
    }
  };

  return (
    <PageShell
      title="Branding"
      description="White-label this tenant: title, colors, and logo. Leave a field blank to inherit the default."
      action={
        <Button onClick={submit} loading={busy} disabled={busy}>
          Save branding
        </Button>
      }
    >
      <div className="mx-auto grid max-w-5xl gap-8 lg:grid-cols-[1fr_20rem]">
        <div className="space-y-6">
          {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}

          <FormField label="App title" htmlFor="b-title" description="Shown in the browser tab and the console. Blank = “DeviceChain”.">
            <Input id="b-title" value={form.title} maxLength={64} placeholder="DeviceChain" onChange={(e) => set('title', e.target.value)} />
          </FormField>

          <div className="grid gap-4 sm:grid-cols-2">
            <ColorField label="Primary" hint="Brand accent (buttons, links, focus rings)." value={form.primary} onChange={(v) => set('primary', v)} />
            <ColorField label="Accent" hint="Secondary highlight surfaces." value={form.accent} onChange={(v) => set('accent', v)} />
            <ColorField label="Sidebar background" hint="Applies to the branded chrome only." value={form.background} onChange={(v) => set('background', v)} />
            <ColorField label="Sidebar foreground" hint="Text/icons on the branded chrome." value={form.foreground} onChange={(v) => set('foreground', v)} />
          </div>

          {contrast !== null && contrast < 4.5 && (
            <p className="text-label-lg text-warning">
              Sidebar foreground/background contrast is {contrast.toFixed(1)}:1 — below the WCAG AA target of
              4.5:1. This is a hint only; you can still save.
            </p>
          )}
          {primaryContrast !== null && primaryContrast < 4.5 && (
            <p className="text-label-lg text-warning">
              Primary is {primaryContrast.toFixed(1)}:1 against white button text — a lighter primary may read
              poorly on buttons. Hint only; you can still save.
            </p>
          )}

          <FormField
            label="Logo"
            description="Upload a PNG/JPEG/WebP (≤1 MB, stored in the object store) or paste an https URL (any image, incl. SVG). Applied immediately — not part of Save."
          >
            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <Input
                  aria-label="Logo URL"
                  value={logoUrl}
                  placeholder="https://…"
                  disabled={logoBusy}
                  onChange={(e) => setLogoUrl(e.target.value)}
                />
                <Button type="button" variant="outline" size="sm" disabled={logoBusy} onClick={applyLogoUrl}>
                  Apply URL
                </Button>
              </div>
              <div className="flex items-center gap-2">
                <UploadButton disabled={logoBusy} onPick={onUpload} />
                {hasOwnLogo && (
                  <Button type="button" variant="ghost" size="sm" disabled={logoBusy} onClick={removeLogo}>
                    <X className="mr-1 size-3.5" /> Remove logo
                  </Button>
                )}
              </div>
            </div>
          </FormField>

          <FormField label="Logo max height (px)" htmlFor="b-height" description="How tall the logo renders in the chip/sidebar (16–200). Blank = 28.">
            <Input id="b-height" type="number" min={16} max={200} value={form.logoMaxHeight} placeholder="28" onChange={(e) => set('logoMaxHeight', e.target.value)} className="w-32" />
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
  const valid = HEX_RE.test(value);
  return (
    <FormField label={label} description={hint}>
      <div className="flex items-center gap-2">
        <input
          type="color"
          aria-label={`${label} swatch`}
          value={valid ? value : '#000000'}
          onChange={(e) => onChange(e.target.value)}
          className="size-9 shrink-0 cursor-pointer rounded border border-border bg-transparent p-0.5"
        />
        <Input value={value} placeholder="inherit" onChange={(e) => onChange(e.target.value)} className="font-mono" />
        {value !== '' && (
          <Button type="button" variant="ghost" size="icon" aria-label={`Clear ${label}`} onClick={() => onChange('')}>
            <X className="size-3.5" />
          </Button>
        )}
      </div>
    </FormField>
  );
}

function UploadButton({ onPick, disabled }: { onPick: (file: File) => void; disabled?: boolean }) {
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
        <Upload className="mr-1 size-3.5" /> Upload
      </Button>
    </>
  );
}

// A live preview of the palette + logo. Colors come from the (unsaved) theme form;
// the logo is the live resolved logo (already applied), shown only in an <img>.
function BrandingPreview({ form, logoSrc }: { form: FormState; logoSrc: string | null }) {
  const primary = HEX_RE.test(form.primary) ? form.primary : undefined;
  const bg = HEX_RE.test(form.background) ? form.background : undefined;
  const fg = HEX_RE.test(form.foreground) ? form.foreground : undefined;
  const height = form.logoMaxHeight.trim() === '' ? 28 : Number(form.logoMaxHeight);
  return (
    <div className="space-y-2">
      <p className="text-sm font-medium text-foreground">Preview</p>
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
            Primary button
          </button>
          <p className="text-xs text-muted-foreground">Sample content on the card surface.</p>
        </div>
      </div>
    </div>
  );
}

function emptyToNull(v: string): string | null {
  return v === '' ? null : v;
}
