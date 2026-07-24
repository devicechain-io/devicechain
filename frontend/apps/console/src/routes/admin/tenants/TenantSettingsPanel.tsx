// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Link } from 'react-router-dom';
import { Trans, useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { SETTING_SOURCE, type AdminTenant } from '@/lib/api/admin';
import { dimensionLabel, dimensionUnit } from '@/routes/common';

// One resolved setting, flattened out of a dimension's rate/burst pair so both
// render through the same row.
type Row = {
  key: string;
  label: string;
  source: string;
  value: number | null;
  tier: number | null;
  override: number | null;
};

// SourceBadge names the level that produced the effective value. This is the half of
// ADR-065 decision 7 that a merged number cannot express: an operator looking at
// "5000" needs to know whether that is what the tier sells or an exception someone
// granted this tenant, because those have very different consequences when the tier
// is later re-priced.
function SourceBadge({ source }: { source: string }) {
  const { t } = useTranslation('tenants');
  if (source === SETTING_SOURCE.override) {
    return <Badge variant="outline">{t('colOverride')}</Badge>;
  }
  if (source === SETTING_SOURCE.tier) {
    return <Badge variant="secondary">{t('colTier')}</Badge>;
  }
  return <Badge variant="secondary">{t('sourcePlatformDefault')}</Badge>;
}

// num renders an optional number, with an em dash for "this level declares none" —
// which is never the same as zero (a zero ceiling admits nothing and is not writable).
function num(v: number | null): string {
  return v == null ? '—' : String(v);
}

// TenantSettingsPanel shows what a tenant is ACTUALLY metered at and why (ADR-065
// decision 7): effective settings resolved as tier + delta, never an opaque merged
// blob. Columns run in cascade order — tier, then the override that may beat it, then
// the winner — so the table reads the way the resolution actually happens.
//
// Every value here is resolved SERVER-side by the same cascade the data plane reads
// its ceilings through; nothing on this screen is recomputed from the tenant's own
// override fields. A second implementation living in the console would eventually
// tell an operator something the platform does not do, which is the one thing a
// screen whose whole job is to be the truth about packaging must never do.
export function TenantSettingsPanel({ tenant }: { tenant: AdminTenant }) {
  const { t } = useTranslation('tenants');
  const rows: Row[] = tenant.effectiveSettings.flatMap((s) => [
    {
      key: `${s.dimension.name}-rate`,
      label: t('common:dimensionRateLabel', {
        label: dimensionLabel(t, s.dimension.name, s.dimension.label),
        unit: dimensionUnit(t, s.dimension.name, s.dimension.rateUnit),
      }),
      source: s.rate.source,
      value: s.rate.value ?? null,
      tier: s.rate.tier ?? null,
      override: s.rate.override ?? null,
    },
    {
      key: `${s.dimension.name}-burst`,
      label: t('common:dimensionBurstLabel', {
        label: dimensionLabel(t, s.dimension.name, s.dimension.label),
      }),
      source: s.burst.source,
      value: s.burst.value ?? null,
      tier: s.burst.tier ?? null,
      override: s.burst.override ?? null,
    },
  ]);

  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">
        <Trans
          t={t}
          i18nKey="settingsExplanation"
          values={{ tier: tenant.tier.name || tenant.tier.token }}
          components={{
            tierLink: (
              <Link to={`/admin/tiers/${encodeURIComponent(tenant.tier.token)}`} className="underline" />
            ),
          }}
        />
      </p>
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>{t('colSetting')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('colTier')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('colOverride')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('colEffective')}</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {rows.map((r) => (
            <DataTableRow key={r.key}>
              <DataTableCell className="font-medium">{r.label}</DataTableCell>
              <DataTableCell className="text-muted-foreground">{num(r.tier)}</DataTableCell>
              <DataTableCell className="text-muted-foreground">{num(r.override)}</DataTableCell>
              <DataTableCell>
                <div className="flex items-center gap-2">
                  {/* A null effective value is not a gap: it means no level declared
                      one, so the enforcing service applies its own default. That
                      number is deliberately not shown — it is not user-management's
                      to state (each service builds it from its own Helm config, and
                      outbound has two independent copies), so a number here would be
                      a third copy that goes stale silently. The label is the honest
                      answer; a wrong number would be worse than none, because an
                      operator would believe it. */}
                  {r.value == null ? (
                    <span className="text-muted-foreground">{t('setByService')}</span>
                  ) : (
                    <span className="font-medium">{r.value}</span>
                  )}
                  <SourceBadge source={r.source} />
                </div>
              </DataTableCell>
            </DataTableRow>
          ))}
        </DataTableBody>
      </DataTable>
    </div>
  );
}
