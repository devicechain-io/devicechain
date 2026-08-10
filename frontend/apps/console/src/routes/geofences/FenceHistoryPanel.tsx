// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A fence as the detection engine saw it, at any version it has ever had.
//
// This is the surface the rest of the design was pointing at. Every mutation
// mints a fence-set version and freezes each fence's geometry into it, and a
// Location event carries the version it was stamped with — so replaying a rule
// against last week's events evaluates it against last week's fences. Without
// that, previewing a geofence rule over history would answer from TODAY's shape
// and quietly be a fiction: confident, plausible, and about a world that never
// existed.
//
// What this panel adds is the ability to see it. The version an operator picks
// is rendered in solid, and the fence's CURRENT shape sits behind it as a dashed
// outline, so "what changed, and when" is a comparison rather than a memory.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useQuery } from '@/lib/hooks/use-query';
import {
  getCurrentFenceSetVersion,
  getGeoFenceSetSnapshot,
  type GeoFence,
} from '@/lib/api/geofences';
import { FenceMap } from './FenceMap';
import { fromGeometryDocument, lookupInSnapshot } from './geometry';

export function FenceHistoryPanel({ entity }: { entity: GeoFence }) {
  const { t } = useTranslation(['entities', 'common']);

  // Fetched once, on mount. If another operator mints a version while this panel
  // is open, "Current" keeps pointing at what WAS latest and "Newer" stays
  // disabled below the real ceiling — accepted, because every other read in this
  // console is fetch-on-mount too and a fence set that moves under a reader is
  // rare. Worth knowing before trusting the badge in a live incident.
  const current = useQuery(() => getCurrentFenceSetVersion(), []);
  const [version, setVersion] = useState<number | null>(null);
  /** Set to '' only while the field is empty mid-edit; null the rest of the time. */
  const [draft, setDraft] = useState<string | null>(null);

  // Null means "not chosen yet", which resolves to the latest once it is known.
  // Seeding from state alone would pin it at 0 before the query answers, and 0 is
  // a meaningful value here — it means no set has ever been minted.
  const latest = current.data ?? 0;
  const chosen = version ?? latest;

  const snapshot = useQuery(
    () => (chosen > 0 ? getGeoFenceSetSnapshot(chosen) : Promise.resolve(null)),
    [chosen],
  );

  const now = fromGeometryDocument(entity.geometry) ?? [];

  // 🔴 error FIRST, and the message passed through as-is. useQuery already
  // extracts the server's words into a string, so running it through errMessage —
  // which only unwraps Error instances — replaced every failure with the generic
  // "Could not reach the server.", making a not-found and a refusal
  // indistinguishable from a dead network.
  if (current.error) return <ErrorBanner message={current.error} />;
  if (current.loading && !current.data) {
    return <p className="text-muted-foreground text-sm">{t('common:loading')}</p>;
  }

  // 🔴 Zero is not "version zero". It means no fence set has ever been minted.
  //
  // Reaching it from a fence's own detail page should be impossible — the fence
  // and its first version are inserted in ONE transaction, so a fence that exists
  // implies a version >= 1. The branch is kept because "impossible" here rests on
  // a transaction boundary in another service, and the cost of being wrong is a
  // stepper pinned at a number nobody can interpret.
  if (latest === 0) {
    return <p className="text-muted-foreground text-sm">{t('entities:geofenceHistoryNone')}</p>;
  }

  // 🔴 THE SNAPSHOT MUST BE THE ONE THAT WAS ASKED FOR.
  //
  // useQuery keeps prior data through a refetch AND through a refetch error. So
  // stepping to a version whose fetch fails used to leave the PREVIOUS version's
  // ring on screen, under the new version number, with the error banner
  // suppressed by a `!snapshot.data` guard — the panel confidently describing a
  // shape that was never the shape at that version. That is the precise lie this
  // panel exists to prevent, and it was invisible because the test transport
  // echoed the latest version rather than the requested one.
  //
  // Comparing the version the server ANSWERED with against the one chosen is what
  // makes stale data unusable rather than merely unlikely.
  const answered = snapshot.data?.version === chosen ? snapshot.data : null;
  const found = answered ? lookupInSnapshot(answered.fences, entity.token) : null;

  return (
    <div className="space-y-4">
      <FormField
        label={t('entities:geofenceHistoryVersion')}
        htmlFor="gf-version"
        description={t('entities:geofenceHistoryVersionHelp', { latest })}
      >
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setDraft(null);
              setVersion(Math.max(1, chosen - 1));
            }}
            disabled={chosen <= 1}
          >
            {t('entities:geofenceHistoryOlder')}
          </Button>
          <Input
            id="gf-version"
            type="number"
            min={1}
            max={latest}
            value={draft ?? String(chosen)}
            className="w-24 tabular-nums"
            onChange={(ev) => {
              const raw = ev.target.value;
              // The draft exists for ONE case: an empty box mid-edit. Number('')
              // is 0, which clamped to 1 and yanked the operator to the oldest
              // version the moment they cleared the field to type a new number.
              // Any non-empty value shows its CLAMPED self, so the number on
              // screen is always the version being displayed.
              if (raw.trim() === '') {
                setDraft('');
                return;
              }
              setDraft(null);
              const n = Number(raw);
              // Clamped rather than validated-with-a-message: the only values that
              // exist are 1..latest, and there is nothing an operator could learn
              // from being told that 900 is not one of them.
              if (Number.isFinite(n)) setVersion(Math.min(latest, Math.max(1, Math.trunc(n))));
            }}
            onBlur={() => setDraft(null)}
          />
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setDraft(null);
              setVersion(Math.min(latest, chosen + 1));
            }}
            disabled={chosen >= latest}
          >
            {t('entities:geofenceHistoryNewer')}
          </Button>
          {chosen === latest && (
            <span className="text-muted-foreground text-xs" data-testid="fence-history-is-current">
              {t('entities:geofenceHistoryCurrent')}
            </span>
          )}
        </div>
      </FormField>

      {snapshot.error && <ErrorBanner message={snapshot.error} />}

      {!snapshot.error && !answered && (
        <p className="text-muted-foreground text-sm" data-testid="fence-history-loading">
          {t('common:loading')}
        </p>
      )}

      {found?.kind === 'absent' && (
        <p className="text-muted-foreground text-sm" data-testid="fence-history-absent">
          {t('entities:geofenceHistoryAbsent', { version: chosen })}
        </p>
      )}
      {found?.kind === 'unreadable' && (
        <p className="text-muted-foreground text-sm" data-testid="fence-history-unreadable">
          {t('entities:geofenceHistoryUnreadable')}
        </p>
      )}

      {found?.kind === 'present' && (
        <div className="space-y-2" data-testid="fence-history-present">
          {/* 🔴 disabled, not merely un-wired: this is a picture of a version that
              has already been frozen. Nothing here can be edited, and a map that
              accepted a drag would imply the past could be rewritten. */}
          <FenceMap vertices={found.vertices} onChange={() => {}} ghost={now} disabled />
          <p className="text-muted-foreground text-xs">
            {t('entities:geofenceHistoryLegend', { version: chosen })}
          </p>
        </div>
      )}
    </div>
  );
}
