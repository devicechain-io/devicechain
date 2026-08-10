// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Self-service tenant basemap editor (ADR-079). A tenant admin (basemap:write) sets
// the tile source their maps draw on — the geofence editor and every dashboard map
// widget — plus the view those maps open at when they have nothing to fit to.
//
// It sits beside Branding rather than on the operator's tenant page, and gated on its
// OWN authority rather than branding:write, because the tile URL carries the TENANT'S
// provider credential: an operator holding it is a bad boundary for a hosted
// deployment, and bundling it with "change the logo" would make each grant imply the
// other in both directions.
//
// The editor writes the RAW override (tenant.basemapOverride, an empty field =
// inherit the operator's instance default) as a full replace, and writes the
// freshly-resolved tenant straight into the tenant cache so every map picks it up
// without a refetch.

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { useCurrentTenant, useSetCurrentTenant } from '@/auth/TenantProvider';
import {
  setTenantBasemap,
  type TenantBasemap,
  type TenantBasemapInput,
} from '@/lib/api/user-management';
import { errMessage } from '@/routes/common';

// The authority name shown in the permission-denied message, in a font-mono span — a
// technical identifier, never localized (mirrors how a token/id is displayed).
const BASEMAP_WRITE_AUTHORITY = 'basemap:write';

// Client-side pre-checks that MIRROR the server rules (the basemap Go package). They
// exist for fail-fast feedback only; the server re-validates everything and is the
// authority. Keeping them in step matters less than keeping them WEAKER — a check
// stricter than the server's would refuse a value the platform accepts.
const TILE_PLACEHOLDERS = [['{z}', '{x}', '{y}'], ['{bbox-epsg-3857}'], ['{quadkey}']];

function looksLikeTemplate(url: string): boolean {
  return TILE_PLACEHOLDERS.some((set) => set.every((token) => url.includes(token)));
}

// The form's per-field state — strings so "" cleanly represents "inherit".
interface FormState {
  tileUrl: string;
  attribution: string;
  centerLat: string;
  centerLon: string;
  zoom: string;
}

function initialState(o: TenantBasemap | null): FormState {
  return {
    tileUrl: o?.tileUrl ?? '',
    attribution: o?.attribution ?? '',
    centerLat: o?.centerLat != null ? String(o.centerLat) : '',
    centerLon: o?.centerLon != null ? String(o.centerLon) : '',
    zoom: o?.zoom != null ? String(o.zoom) : '',
  };
}

function emptyToNull(v: string): string | null {
  return v === '' ? null : v;
}

function numberOrNull(v: string): number | null {
  const trimmed = v.trim();
  return trimmed === '' ? null : Number(trimmed);
}

export default function BasemapPage() {
  const { t } = useTranslation('basemap');
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'basemap:write');
  const tenant = useCurrentTenant();

  if (!canWrite) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <p className="text-sm text-muted-foreground">
          {t('noPermissionPrefix')}
          <span className="font-mono"> {BASEMAP_WRITE_AUTHORITY} </span>
          {t('noPermissionSuffix')}
        </p>
      </PageShell>
    );
  }

  // 🔴 Seed only from a REAL fetched override, never from a blank. A fetched tenant
  // always carries a non-null basemapOverride (GraphQL `TenantBasemap!`), so null
  // here means the tenant fetch has not landed — and seeding the form blank would
  // make the next Save full-replace-CLEAR whatever the tenant had configured. Same
  // trap the branding editor documents; same answer.
  const override = tenant?.basemapOverride ?? null;
  if (!override) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <LoadingState description={t('loading')} />
      </PageShell>
    );
  }
  return <BasemapEditor key={tenant?.token} override={override} />;
}

function BasemapEditor({ override }: { override: TenantBasemap }) {
  const { t } = useTranslation('basemap');
  const applyTenant = useSetCurrentTenant();
  const tenant = useCurrentTenant();
  const { toast } = useToast();

  const [form, setForm] = useState<FormState>(() => initialState(override));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Re-seed a pristine form when a newer override arrives (a stale cached tenant, or
  // another admin's change); never once the user has started editing.
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (!dirty) setForm(initialState(override));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [override.tileUrl, override.attribution, override.centerLat, override.centerLon, override.zoom]);

  const set = (k: keyof FormState, v: string) => {
    setDirty(true);
    setForm((f) => ({ ...f, [k]: v }));
  };
  // Bound per-field setters, defined outside JSX so the literal key never appears as a
  // JSX-attribute call argument (which trips the i18n literal-string lint, even for a
  // technical identifier).
  const setTileUrlField = (v: string) => set('tileUrl', v);
  const setAttributionField = (v: string) => set('attribution', v);
  const setCenterLatField = (v: string) => set('centerLat', v);
  const setCenterLonField = (v: string) => set('centerLon', v);
  const setZoomField = (v: string) => set('zoom', v);

  const tileUrl = form.tileUrl.trim();
  const attribution = form.attribution.trim();

  // The tile source is ONE value: both halves or neither. This mirrors the server,
  // and the message says WHY rather than just naming the field — an operator who
  // reads "attribution is required" and does not know the reason will look for a way
  // to turn the requirement off.
  const missingAttribution = tileUrl !== '' && attribution === '';
  const orphanAttribution = tileUrl === '' && attribution !== '';
  const badScheme = tileUrl !== '' && !tileUrl.startsWith('https://');
  const notATemplate = tileUrl !== '' && !badScheme && !looksLikeTemplate(tileUrl);
  // A coordinate is a pair; half of one names no point.
  const halfCoordinate =
    (form.centerLat.trim() === '') !== (form.centerLon.trim() === '');

  // 🔴 A non-numeric camera field must BLOCK, not fall through. `Number('abc')` is
  // NaN, JSON.stringify turns NaN into null on the wire, and the mutation is a full
  // replace — so without this, typing a stray character into Zoom and pressing Save
  // silently CLEARS the stored zoom and reports success. That is #704's field-loss
  // shape in miniature: a value the operator never meant to clear, cleared quietly.
  const badNumbers = (['centerLat', 'centerLon', 'zoom'] as const).filter(
    (k) => form[k].trim() !== '' && !Number.isFinite(Number(form[k].trim())),
  );

  const blocked =
    missingAttribution ||
    orphanAttribution ||
    badScheme ||
    notATemplate ||
    halfCoordinate ||
    badNumbers.length > 0;

  const submit = async () => {
    setFormError(null);
    if (blocked) return;
    const input: Required<TenantBasemapInput> = {
      tileUrl: emptyToNull(tileUrl),
      attribution: emptyToNull(attribution),
      centerLat: numberOrNull(form.centerLat),
      centerLon: numberOrNull(form.centerLon),
      zoom: numberOrNull(form.zoom),
    };
    setBusy(true);
    try {
      const updated = await setTenantBasemap(input);
      applyTenant(updated);
      setDirty(false);
      toast(t('savedToast'));
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  // 🔴 `tenant.basemap` is the EFFECTIVE value, which already includes this tenant's
  // own override — so it is only the instance default when this tenant stores none.
  // The hint is therefore gated on the STORED override being empty, not on the field
  // being empty. Without that gate, a tenant with its own URL who clears the field
  // sees "Inheriting the instance default: <its own URL>", which is wrong twice over:
  // that URL is not the instance default, and saving will not keep it.
  const inherited = tenant?.basemap ?? null;
  const isInheriting = tileUrl === '' && !override.tileUrl && !!inherited?.tileUrl;

  return (
    <PageShell title={t('title')} description={t('description')}>
      <div className="max-w-2xl space-y-6">
        {formError && <ErrorBanner message={formError} />}

        <section className="space-y-4">
          <h2 className="text-sm font-medium">{t('tileSourceHeading')}</h2>
          <p className="text-muted-foreground text-xs">{t('tileSourceHelp')}</p>

          <FormField
            label={t('tileUrl')}
            htmlFor="bm-tile-url"
            description={t('tileUrlHelp')}
          >
            <Input
              id="bm-tile-url"
              value={form.tileUrl}
              onChange={(ev) => setTileUrlField(ev.target.value)}
              placeholder={t('tileUrlPlaceholder')}
            />
          </FormField>
          {badScheme && <ErrorBanner message={t('errHttps')} />}
          {notATemplate && <ErrorBanner message={t('errNotATemplate')} />}

          <FormField
            label={t('attribution')}
            htmlFor="bm-attribution"
            description={t('attributionHelp')}
          >
            <Input
              id="bm-attribution"
              value={form.attribution}
              onChange={(ev) => setAttributionField(ev.target.value)}
              placeholder={t('attributionPlaceholder')}
            />
          </FormField>
          {missingAttribution && <ErrorBanner message={t('errAttributionRequired')} />}
          {orphanAttribution && <ErrorBanner message={t('errAttributionOrphan')} />}

          {isInheriting && (
            <p className="text-muted-foreground text-xs" data-testid="basemap-inheriting">
              {t('inheriting', { tileUrl: inherited?.tileUrl ?? '' })}
            </p>
          )}
          {/* 🔴 Said plainly, because this change makes configuring a provider key
              far more common than it was: the tile URL is not a secret and cannot be
              one — the browser has to fetch tiles with it. */}
          <p className="text-muted-foreground text-xs">{t('keyVisibilityWarning')}</p>
        </section>

        <section className="space-y-4">
          <h2 className="text-sm font-medium">{t('viewHeading')}</h2>
          <p className="text-muted-foreground text-xs">{t('viewHelp')}</p>

          <div className="grid grid-cols-3 gap-3">
            <FormField label={t('centerLat')} htmlFor="bm-center-lat">
              <Input
                id="bm-center-lat"
                value={form.centerLat}
                onChange={(ev) => setCenterLatField(ev.target.value)}
                inputMode="decimal"
              />
            </FormField>
            <FormField label={t('centerLon')} htmlFor="bm-center-lon">
              <Input
                id="bm-center-lon"
                value={form.centerLon}
                onChange={(ev) => setCenterLonField(ev.target.value)}
                inputMode="decimal"
              />
            </FormField>
            <FormField label={t('zoom')} htmlFor="bm-zoom">
              <Input
                id="bm-zoom"
                value={form.zoom}
                onChange={(ev) => setZoomField(ev.target.value)}
                inputMode="decimal"
              />
            </FormField>
          </div>
          {halfCoordinate && <ErrorBanner message={t('errHalfCoordinate')} />}
          {badNumbers.length > 0 && (
            <ErrorBanner message={t('errNotANumber', { fields: badNumbers.join(', ') })} />
          )}
        </section>

        <div className="flex gap-2">
          <Button onClick={submit} loading={busy} disabled={blocked}>
            {t('save')}
          </Button>
        </div>
      </div>
    </PageShell>
  );
}
