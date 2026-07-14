// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Self-service tenant white-labeling editor (ADR-038 Phase 2, slice 38c). A
// tenant admin (branding:write) edits their OWN tenant's title, palette, and logo.
// It edits the RAW override (tenant.brandingOverride): an empty field means
// "inherit" (clear the override), so leaving everything blank restores the stock
// DeviceChain look. The server is authoritative — client checks here are fail-fast
// UX only. On save the mutation returns the freshly-resolved tenant, which we write
// straight into the tenant cache so the rebrand shows across the shell immediately.

import { useMemo, useRef, useState } from 'react';
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
  setTenantBranding,
  type TenantBranding,
  type TenantBrandingInput,
} from '@/lib/api/user-management';
import { contrastRatio } from '@/lib/branding';
import { errMessage } from '@/routes/common';

// Mirrors the server allow-list (branding package): inline logos are raster-only
// (SVG must be an https URL) and capped on DECODED bytes. Client pre-check only.
const INLINE_LOGO_MIME = ['image/png', 'image/jpeg', 'image/webp'];
const MAX_LOGO_BYTES = 256 * 1024;
const HEX_RE = /^#[0-9a-fA-F]{6}$/;

// The editor's per-field state — strings so "" cleanly represents "inherit".
interface FormState {
  title: string;
  logo: string;
  logoMaxHeight: string;
  primary: string;
  background: string;
  foreground: string;
  accent: string;
}

function initialState(o: {
  title?: string | null;
  logo?: string | null;
  logoMaxHeight?: number | null;
  primary?: string | null;
  background?: string | null;
  foreground?: string | null;
  accent?: string | null;
} | null): FormState {
  return {
    title: o?.title ?? '',
    logo: o?.logo ?? '',
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
  // seed the form blank, which would make a save full-replace-clear every existing
  // override (setTenantBranding is a full replace; a blank field = clear). Keying
  // the editor by token+updatedAt re-seeds it if the tenant is refetched/rebranded.
  const override = tenant?.brandingOverride ?? null;
  if (!override) {
    return (
      <PageShell title="Branding" description="Tenant white-labeling">
        <LoadingState description="Loading branding…" />
      </PageShell>
    );
  }
  return <BrandingEditor key={`${tenant?.token}:${override.updatedAt ?? ''}`} override={override} />;
}

function BrandingEditor({ override }: { override: TenantBranding }) {
  const applyTenant = useSetCurrentTenant();
  const toast = useToast();

  const [form, setForm] = useState<FormState>(() => initialState(override));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const set = (k: keyof FormState, v: string) => setForm((f) => ({ ...f, [k]: v }));

  // Client-side hex validation (fail-fast); the server re-validates.
  const badHex = (['primary', 'background', 'foreground', 'accent'] as const).filter(
    (k) => form[k] !== '' && !HEX_RE.test(form[k]),
  );

  // Non-blocking contrast hints (guidance only, never block a save — ADR-038 §4):
  // the sidebar foreground-on-background pair, and primary against its always-white
  // foreground (--primary-foreground is white in both themes, so a light primary
  // gives unreadable white-on-light button text with no other warning).
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
      logo: emptyToNull(form.logo.trim()),
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
      toast.toast('Branding saved');
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const onPickFile = async (file: File) => {
    setFormError(null);
    if (!INLINE_LOGO_MIME.includes(file.type)) {
      setFormError('Inline logos must be PNG, JPEG, or WebP. For SVG, host it and paste an https URL.');
      return;
    }
    if (file.size > MAX_LOGO_BYTES) {
      setFormError(`Logo must be at most ${MAX_LOGO_BYTES / 1024} KB (this file is ${Math.round(file.size / 1024)} KB). Host larger logos and paste an https URL.`);
      return;
    }
    try {
      const dataUri = await readAsDataURL(file);
      set('logo', dataUri);
    } catch {
      setFormError('Could not read that file.');
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

          <FormField label="Logo" htmlFor="b-logo" description="An https URL (any image, incl. SVG) or upload a PNG/JPEG/WebP (≤256 KB, stored inline).">
            <div className="space-y-2">
              <Input id="b-logo" value={form.logo} placeholder="https://…" onChange={(e) => set('logo', e.target.value)} />
              <div className="flex items-center gap-2">
                <UploadButton onPick={onPickFile} />
                {form.logo && (
                  <Button type="button" variant="ghost" size="sm" onClick={() => set('logo', '')}>
                    <X className="mr-1 size-3.5" /> Clear
                  </Button>
                )}
              </div>
            </div>
          </FormField>

          <FormField label="Logo max height (px)" htmlFor="b-height" description="How tall the logo renders in the chip/sidebar (16–200). Blank = 28.">
            <Input id="b-height" type="number" min={16} max={200} value={form.logoMaxHeight} placeholder="28" onChange={(e) => set('logoMaxHeight', e.target.value)} className="w-32" />
          </FormField>
        </div>

        <BrandingPreview form={form} />
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

function UploadButton({ onPick }: { onPick: (file: File) => void }) {
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
      <Button type="button" variant="outline" size="sm" onClick={() => ref.current?.click()}>
        <Upload className="mr-1 size-3.5" /> Upload
      </Button>
    </>
  );
}

// A live preview of the palette + logo, rendered only from the form state (never
// applied to the real shell until save). The logo is shown only in an <img>.
function BrandingPreview({ form }: { form: FormState }) {
  const primary = HEX_RE.test(form.primary) ? form.primary : undefined;
  const bg = HEX_RE.test(form.background) ? form.background : undefined;
  const fg = HEX_RE.test(form.foreground) ? form.foreground : undefined;
  const height = form.logoMaxHeight.trim() === '' ? 28 : Number(form.logoMaxHeight);
  return (
    <div className="space-y-2">
      <p className="text-sm font-medium text-foreground">Preview</p>
      <div className="overflow-hidden rounded-lg border border-border">
        <div className="flex items-center gap-2 px-3 py-3" style={{ background: bg, color: fg }}>
          {form.logo ? (
            <img src={form.logo} alt="" className="w-auto max-w-[70%] object-contain" style={{ maxHeight: height }} />
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

function readAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
}
