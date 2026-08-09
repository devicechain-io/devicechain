// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A device's last-known position — the O(1) projection from device-state, not a
// scan of its location history.
//
// Two rules shape everything here:
//
// 🔴 ABSENT IS NOT ZERO. Elevation, accuracy, speed and heading are each
// optional: a device reports what its receiver knows. A fix with no heading has
// not reported due north, and rendering `0°` would invent a fact the platform
// deliberately declined to store — the operator cannot tell the invented value
// from a real one. Every optional is tested with `!= null`, which admits a
// genuine 0 and rejects an absence, and an absent field gets NO ROW at all.
//
// 🔴 REFUSED IS NOT EMPTY. Position is gated on its own `location:read`
// authority, which is deliberately absent from the read-only viewer baseline, so
// being refused here is an ordinary state rather than a fault. "No position
// recorded" would be a statement about the DEVICE; the truth is a statement
// about the CALLER, so the refusal gets its own copy and its own look — neither
// the empty state's wording nor the error state's alarm.
//
// 🔴 UNDECLARED IS NOT "NO POSITION" EITHER — and this is why the panel owns its
// own SectionPanel and can render NOTHING. A profile may declare that its devices
// do not report position, and a device of that kind should not get a position
// panel at all. But the declaration is DESCRIPTIVE: ingest never gates on it, so a
// device that reports a fix before anyone declares it keeps every fix it sent.
// Hiding on the declaration alone would therefore hide REAL, STORED DATA behind an
// unedited profile — the exact failure the platform refuses to make at ingest, and
// it must not be reintroduced one layer up in the UI. Hence the rule below.
import { useTranslation } from 'react-i18next';
import { Lock } from 'lucide-react';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { useQuery } from '@/lib/hooks/use-query';
import { SectionPanel } from '@/components/ui/section-panel';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { getLatestLocation, type LatestLocation, type LatestLocationResult } from '@/lib/api/device-state';
import { getDeviceLocationDeclaration, type DeviceLocationCapability } from '@/lib/api/device-management';
import { formatTime } from '@/lib/utils';

// The authority that gates every position surface on the platform.
const LOCATION_READ = 'location:read';

/**
 * THE HIDE RULE, stated once.
 *
 * The panel disappears only when BOTH are true:
 *
 *   1. the device's profile — its ACTIVE PUBLISHED version, never the draft —
 *      declares no position, AND
 *   2. the server positively answered that no position is stored.
 *
 * Both halves are load-bearing:
 *
 *   - Without (1) the panel would show for every device on the platform, which is
 *     the state this slice exists to end.
 *   - Without (2) an undeclared device that IS reporting position would have its
 *     real, stored fixes hidden by an unedited profile. A declaration is metadata
 *     an operator may simply not have written yet; data is data.
 *
 * Everything that is not a definite "no" keeps the panel: a `forbidden` position
 * read is NOT evidence of absence (it says the CALLER may not look, not that the
 * device never moved), a still-loading or failed declaration read is not evidence
 * either, and a null capability (the token resolved to no device) says nothing
 * about position at all. The rule fails OPEN toward showing, because showing an
 * empty panel is a cosmetic cost and hiding a fix is a correctness one.
 */
function panelIsHidden(
  capability: DeviceLocationCapability | null,
  position: LatestLocationResult | null,
): boolean {
  return capability?.declared === false && position?.kind === 'never-located';
}

export function DeviceLocationPanel({ deviceToken }: { deviceToken: string }) {
  const { t } = useTranslation('devices');
  const { claims } = useAuth();
  // Check the claim BEFORE asking, so the ordinary "not granted" case costs no
  // round-trip and never flashes an error. The server refusal below is still
  // handled: a token issued before a role change carries stale authorities, so
  // the claim can say yes where the server says no.
  const mayReadLocation = hasAuthority(claims, LOCATION_READ);

  const { data, loading, error } = useQuery<LatestLocationResult>(
    () =>
      mayReadLocation
        ? getLatestLocation(deviceToken)
        : Promise.resolve<LatestLocationResult>({ kind: 'forbidden' }),
    [deviceToken, mayReadLocation],
  );

  // Whether this KIND of device reports position at all, read through the published
  // profile version. Gated on device:read, so every viewer can ask — a user who may
  // not see coordinates can still be told the panel is irrelevant here.
  //
  // Its failure is swallowed on purpose: this query only ever decides whether to
  // HIDE, so a failed metadata lookup must degrade to showing the position panel,
  // never to an error where the position itself loaded fine.
  const { data: capability, loading: capabilityLoading } = useQuery<DeviceLocationCapability>(
    () => getDeviceLocationDeclaration(deviceToken).catch(() => null),
    [deviceToken],
  );

  // The hide decision is still open while the declaration is in flight, so render
  // nothing rather than flashing a panel that is about to be removed. Once the
  // declaration says the device DOES report position the panel is staying, so the
  // position's own load shows a spinner inside it as before; only an undeclared
  // device has to wait for the position too, because that is the one case where the
  // answer can still go either way.
  if (capabilityLoading) return null;
  if (capability?.declared === false && loading) return null;
  if (panelIsHidden(capability, data)) return null;

  return (
    <SectionPanel title={t('latestPositionTitle')}>
      <LocationBody data={data} loading={loading} error={error} />
    </SectionPanel>
  );
}

// LocationBody renders the position itself, once the panel has decided it exists.
function LocationBody({
  data,
  loading,
  error,
}: {
  data: LatestLocationResult | null;
  loading: boolean;
  error: string | null;
}) {
  const { t } = useTranslation('devices');

  if (loading) return <LoadingState description={t('loadingLocation')} />;
  if (error) return <ErrorState description={error} />;
  if (!data) return <EmptyState description={t('noLocationRecorded')} />;
  if (data.kind === 'forbidden') {
    return (
      <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
        <Lock size={14} />
        <span>{t('locationNoPermission')}</span>
      </div>
    );
  }
  if (data.kind === 'never-located') {
    return <EmptyState description={t('noLocationRecorded')} />;
  }

  return <LocationFix fix={data.location} />;
}

// LocationFix renders the reported fields of one fix, and only those. Split out
// so the display rule (absent ⇒ no row) is readable on its own, away from the
// loading/permission branching above.
function LocationFix({ fix }: { fix: LatestLocation }) {
  const { t } = useTranslation('devices');

  // `!= null` — NOT a truthiness test. A truthy test would drop latitude 0 (the
  // equator), longitude 0 (Greenwich), elevation 0 (sea level), speed 0 (parked)
  // and heading 0 (due north), every one of which is a real, reported reading.
  const rows: { labelKey: string; value: string }[] = [];
  if (fix.latitude != null) {
    rows.push({ labelKey: 'locationLatitude', value: formatDegrees(fix.latitude) });
  }
  if (fix.longitude != null) {
    rows.push({ labelKey: 'locationLongitude', value: formatDegrees(fix.longitude) });
  }
  if (fix.elevation != null) {
    rows.push({ labelKey: 'locationElevation', value: t('locationMeters', { value: fix.elevation }) });
  }
  if (fix.accuracy != null) {
    rows.push({ labelKey: 'locationAccuracy', value: t('locationAccuracyMeters', { value: fix.accuracy }) });
  }
  if (fix.speed != null) {
    rows.push({ labelKey: 'locationSpeed', value: t('locationMetersPerSecond', { value: fix.speed }) });
  }
  if (fix.heading != null) {
    rows.push({ labelKey: 'locationHeading', value: t('locationDegrees', { value: fix.heading }) });
  }

  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
      {rows.map((row) => (
        <div key={row.labelKey} className="contents">
          <dt className="text-muted-foreground">{t(row.labelKey)}</dt>
          <dd className="font-mono text-foreground">{row.value}</dd>
        </div>
      ))}
      <dt className="text-muted-foreground">{t('locationReportedAt')}</dt>
      <dd className="text-foreground">{formatTime(fix.occurredTime)}</dd>
    </dl>
  );
}

// Coordinates are WGS84 decimal degrees platform-wide. Six decimal places is
// ~0.1 m at the equator — finer than any consumer receiver — and fixed notation
// keeps a column of fixes aligned and stops a small value rendering in
// exponential form.
function formatDegrees(value: number): string {
  return value.toFixed(6);
}
