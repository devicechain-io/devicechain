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
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { useCurrentTenant, useSetCurrentTenant } from '@/auth/TenantProvider';
import {
  BasemapFields,
  basemapProblems,
  type BasemapFormState,
} from '@/components/basemap/BasemapFields';
import {
  setTenantBasemap,
  type TenantBasemap,
  type TenantBasemapInput,
} from '@/lib/api/user-management';
import { errMessage } from '@/routes/common';

// The authority name shown in the permission-denied message, in a font-mono span — a
// technical identifier, never localized (mirrors how a token/id is displayed).
const BASEMAP_WRITE_AUTHORITY = 'basemap:write';

// Seeds the shared form from a fetched tenant override.
function initialState(o: TenantBasemap): BasemapFormState {
  return {
    tileUrl: o.tileUrl ?? '',
    attribution: o.attribution ?? '',
    centerLat: o.centerLat != null ? String(o.centerLat) : '',
    centerLon: o.centerLon != null ? String(o.centerLon) : '',
    zoom: o.zoom != null ? String(o.zoom) : '',
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

  const [form, setForm] = useState<BasemapFormState>(() => initialState(override));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Re-seed a pristine form when a newer override arrives (a stale cached tenant, or
  // another admin's change); never once the user has started editing.
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (!dirty) setForm(initialState(override));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [override.tileUrl, override.attribution, override.centerLat, override.centerLon, override.zoom]);

  const change = (next: BasemapFormState) => {
    setDirty(true);
    setForm(next);
  };

  const problems = basemapProblems(form);
  const blocked =
    problems.missingAttribution ||
    problems.orphanAttribution ||
    problems.badScheme ||
    problems.notATemplate ||
    problems.unsubstitutedKey ||
    problems.halfCoordinate ||
    problems.badNumbers.length > 0;

  const submit = async () => {
    setFormError(null);
    if (blocked) return;
    const input: Required<TenantBasemapInput> = {
      tileUrl: emptyToNull(form.tileUrl.trim()),
      attribution: emptyToNull(form.attribution.trim()),
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
  const isInheriting = form.tileUrl.trim() === '' && !override.tileUrl && !!inherited?.tileUrl;

  return (
    <PageShell title={t('title')} description={t('description')}>
      <div className="max-w-2xl space-y-6">
        {formError && <ErrorBanner message={formError} />}

        <BasemapFields
          value={form}
          onChange={change}
          inheritedTileUrl={isInheriting ? (inherited?.tileUrl ?? null) : null}
        />

        <div className="flex gap-2">
          <Button onClick={submit} loading={busy} disabled={blocked}>
            {t('save')}
          </Button>
        </div>
      </div>
    </PageShell>
  );
}
