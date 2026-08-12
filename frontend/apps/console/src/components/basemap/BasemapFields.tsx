// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The basemap form — the provider picker, the tile source, and the fallback view
// — shared by the two tiers that configure one (ADR-079):
//
//   the TENANT's own basemap        BasemapPage
//   the INSTANCE default            the basemap.default system setting
//
// They are the same fields under the same rules, so they are the same component.
// What differs is only what an empty field INHERITS, and that is a prop rather
// than an assumption: for a tenant, blank means "use the operator's default"; for
// the operator, blank means "the console draws no map for anyone who has not set
// one". A component that guessed would tell one of the two tiers something false.
//
// 🔴 The checks here MIRROR the server (the basemap Go package) for fail-fast
// feedback; the server re-validates and is the authority. Keeping them in step
// matters less than keeping them WEAKER — a check stricter than the server's
// would refuse a value the platform accepts.

import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { BasemapPicker, type TileSource } from '@/components/basemap/BasemapPicker';
import { API_KEY_TOKEN } from '@/components/basemap/catalog';

/** The form's per-field state — strings so "" cleanly represents "inherit". */
export interface BasemapFormState {
  tileUrl: string;
  attribution: string;
  centerLat: string;
  centerLon: string;
  zoom: string;
}

const TILE_PLACEHOLDERS = [['{z}', '{x}', '{y}'], ['{bbox-epsg-3857}'], ['{quadkey}']];

function looksLikeTemplate(url: string): boolean {
  return TILE_PLACEHOLDERS.some((set) => set.every((token) => url.includes(token)));
}

/**
 * Every rule a basemap form can break, as i18n keys in the `basemap` namespace.
 * Returned as a set so the fields can each render their own, and a caller gating
 * a Save button can ask whether there are any.
 */
export interface BasemapProblems {
  missingAttribution: boolean;
  orphanAttribution: boolean;
  badScheme: boolean;
  notATemplate: boolean;
  unsubstitutedKey: boolean;
  halfCoordinate: boolean;
  /** The camera fields that are neither empty nor a number. */
  badNumbers: string[];
}

/** Every rule satisfied — what a caller that renders its own messages sees. */
const NO_PROBLEMS: BasemapProblems = {
  missingAttribution: false,
  orphanAttribution: false,
  badScheme: false,
  notATemplate: false,
  unsubstitutedKey: false,
  halfCoordinate: false,
  badNumbers: [],
};

export function basemapProblems(form: BasemapFormState): BasemapProblems {
  const tileUrl = form.tileUrl.trim();
  const attribution = form.attribution.trim();
  const badScheme = tileUrl !== '' && !tileUrl.startsWith('https://');
  return {
    // The tile source is ONE value: both halves or neither. This mirrors the
    // server, and the messages say WHY rather than just naming the field — an
    // operator who reads "attribution is required" and does not know the reason
    // will look for a way to turn the requirement off.
    missingAttribution: tileUrl !== '' && attribution === '',
    orphanAttribution: tileUrl === '' && attribution !== '',
    badScheme,
    notATemplate: tileUrl !== '' && !badScheme && !looksLikeTemplate(tileUrl),
    // A catalog provider was chosen but its key was never filled in, so the
    // template still carries the placeholder. The server refuses this too —
    // {apiKey} is not a token the renderer substitutes — but catching it here
    // names the actual problem ("this provider needs a key") instead of reporting
    // an unknown placeholder.
    unsubstitutedKey: tileUrl.includes(API_KEY_TOKEN),
    // A coordinate is a pair; half of one names no point.
    halfCoordinate: (form.centerLat.trim() === '') !== (form.centerLon.trim() === ''),
    // 🔴 A non-numeric camera field must BLOCK, not fall through. `Number('abc')`
    // is NaN, JSON.stringify turns NaN into null on the wire, and the save is a
    // full replace — so without this, typing a stray character into Zoom and
    // pressing Save silently CLEARS the stored zoom and reports success.
    badNumbers: (['centerLat', 'centerLon', 'zoom'] as const).filter(
      (k) => form[k].trim() !== '' && !Number.isFinite(Number(form[k].trim())),
    ),
  };
}

/** The first problem's i18n key (in the `basemap` namespace), or null. */
export function firstBasemapProblemKey(form: BasemapFormState): string | null {
  const p = basemapProblems(form);
  if (p.badScheme) return 'basemap:errHttps';
  if (p.notATemplate) return 'basemap:errNotATemplate';
  if (p.unsubstitutedKey) return 'basemap:errUnsubstitutedKey';
  if (p.missingAttribution) return 'basemap:errAttributionRequired';
  if (p.orphanAttribution) return 'basemap:errAttributionOrphan';
  if (p.halfCoordinate) return 'basemap:errHalfCoordinate';
  if (p.badNumbers.length > 0) return 'basemap:errNotANumber';
  return null;
}

export function BasemapFields({
  value,
  onChange,
  /**
   * What an empty tile URL inherits, shown as a hint. Null when nothing is
   * inherited — which is the operator tier's truth, not a missing value.
   */
  inheritedTileUrl,
  /**
   * Whether to render each rule's message beside its field. Default true, which
   * is what the tenant page wants. The system-settings editor passes false: its
   * frame already shows the blocking reason in one place for every setting, and
   * showing it twice on one screen reads as two problems.
   */
  showProblems = true,
}: {
  value: BasemapFormState;
  onChange: (next: BasemapFormState) => void;
  inheritedTileUrl?: string | null;
  showProblems?: boolean;
}) {
  const { t } = useTranslation('basemap');
  const problems = showProblems ? basemapProblems(value) : NO_PROBLEMS;

  const set = (patch: Partial<BasemapFormState>) => onChange({ ...value, ...patch });
  // Bound per-field setters, defined outside JSX so the literal key never appears
  // as a JSX-attribute call argument (which trips the i18n literal-string lint).
  const setTileUrlField = (v: string) => set({ tileUrl: v });
  const setAttributionField = (v: string) => set({ attribution: v });
  const setCenterLatField = (v: string) => set({ centerLat: v });
  const setCenterLonField = (v: string) => set({ centerLon: v });
  const setZoomField = (v: string) => set({ zoom: v });
  // 🔴 The picker writes BOTH halves in ONE update, never two `set` calls.
  //
  // Not for a rendering reason — React batches updates within an event handler, so
  // two calls would produce one render either way. The reason is that the tile
  // source IS one value: the same rule the server's Merge enforces across tiers,
  // and the reason an emitted pair can never be split. Writing it as one update
  // means there is no code path, now or after a refactor that moves one of these
  // calls, in which a URL is stored under the previous provider's credit line.
  const setTileSource = (next: TileSource) =>
    set({ tileUrl: next.tileUrl, attribution: next.attribution });

  return (
    <>
      <section className="space-y-4">
        <h2 className="text-sm font-medium">{t('tileSourceHeading')}</h2>
        <p className="text-muted-foreground text-xs">{t('tileSourceHelp')}</p>

        {/* The picker prefills the two fields below rather than replacing them:
            choosing a provider is the easy path, and the raw fields stay editable
            for a private tile server or a provider the catalog does not carry. */}
        <BasemapPicker
          tileUrl={value.tileUrl}
          attribution={value.attribution}
          onChange={setTileSource}
        />

        <FormField label={t('tileUrl')} htmlFor="bm-tile-url" description={t('tileUrlHelp')}>
          <Input
            id="bm-tile-url"
            value={value.tileUrl}
            onChange={(ev) => setTileUrlField(ev.target.value)}
            placeholder={t('tileUrlPlaceholder')}
          />
        </FormField>
        {problems.badScheme && <ErrorBanner message={t('errHttps')} />}
        {problems.notATemplate && <ErrorBanner message={t('errNotATemplate')} />}
        {problems.unsubstitutedKey && <ErrorBanner message={t('errUnsubstitutedKey')} />}

        <FormField
          label={t('attribution')}
          htmlFor="bm-attribution"
          description={t('attributionHelp')}
        >
          <Input
            id="bm-attribution"
            value={value.attribution}
            onChange={(ev) => setAttributionField(ev.target.value)}
            placeholder={t('attributionPlaceholder')}
          />
        </FormField>
        {problems.missingAttribution && <ErrorBanner message={t('errAttributionRequired')} />}
        {problems.orphanAttribution && <ErrorBanner message={t('errAttributionOrphan')} />}

        {inheritedTileUrl && (
          <p className="text-muted-foreground text-xs" data-testid="basemap-inheriting">
            {t('inheriting', { tileUrl: inheritedTileUrl })}
          </p>
        )}
        {/* 🔴 Said plainly, because the provider catalog makes configuring a
            provider key far more common than it was: the tile URL is not a secret
            and cannot be one — the browser has to fetch tiles with it. */}
        <p className="text-muted-foreground text-xs">{t('keyVisibilityWarning')}</p>
      </section>

      <section className="space-y-4">
        <h2 className="text-sm font-medium">{t('viewHeading')}</h2>
        <p className="text-muted-foreground text-xs">{t('viewHelp')}</p>

        <div className="grid grid-cols-3 gap-3">
          <FormField label={t('centerLat')} htmlFor="bm-center-lat">
            <Input
              id="bm-center-lat"
              value={value.centerLat}
              onChange={(ev) => setCenterLatField(ev.target.value)}
              inputMode="decimal"
            />
          </FormField>
          <FormField label={t('centerLon')} htmlFor="bm-center-lon">
            <Input
              id="bm-center-lon"
              value={value.centerLon}
              onChange={(ev) => setCenterLonField(ev.target.value)}
              inputMode="decimal"
            />
          </FormField>
          <FormField label={t('zoom')} htmlFor="bm-zoom">
            <Input
              id="bm-zoom"
              value={value.zoom}
              onChange={(ev) => setZoomField(ev.target.value)}
              inputMode="decimal"
            />
          </FormField>
        </div>
        {problems.halfCoordinate && <ErrorBanner message={t('errHalfCoordinate')} />}
        {problems.badNumbers.length > 0 && (
          <ErrorBanner message={t('errNotANumber', { fields: problems.badNumbers.join(', ') })} />
        )}
      </section>
    </>
  );
}
