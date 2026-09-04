// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The geofence create/edit form. Follows the registry type-form shape (token,
// name, description, one state object, one submit, inline error banner) and adds
// the drawing surface, which is the only part that is not generic.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { errMessage } from '@/routes/common';
import { Textarea } from '@/components/ui/textarea';
import {
  createGeoFence,
  updateGeoFence,
  type GeoFence,
  type GeoFenceCreateRequest,
} from '@/lib/api/geofences';
import { fallbackView, renderableBasemap, resolveBasemap } from '@devicechain/client';
import { useTenantBasemap } from '@/auth/TenantProvider';
import { FenceMap } from './FenceMap';
import {
  MAX_FENCE_VERTICES,
  MIN_VERTICES,
  checkGeometry,
  fromGeometryDocument,
  toGeometryDocument,
  type GeometryProblem,
  type Vertex,
} from './geometry';

// The basemap now cascades (ADR-079): the tenant owns one, and these two keys are a
// PERSONAL override on top of it — how someone tries a provider in their own browser
// before committing it tenant-wide. They are not a per-fence field and never were.
//
// There is still deliberately no platform default tile source: shipping one would
// point every self-hosted instance at a public tile service that never agreed to
// serve it. What changed is that a blank field no longer means a blank map — it
// means "use whatever the tenant configured".
const TILE_URL_KEY = 'dc.geofence.tileUrl';
const TILE_ATTRIBUTION_KEY = 'dc.geofence.tileAttribution';

function readPreference(key: string): string {
  try {
    return window.localStorage.getItem(key) ?? '';
  } catch {
    // A browser with storage disabled must still be able to draw a fence.
    return '';
  }
}

function writePreference(key: string, value: string) {
  try {
    if (value) window.localStorage.setItem(key, value);
    else window.localStorage.removeItem(key);
  } catch {
    /* preference is a convenience; losing it must never fail a save */
  }
}

const PROBLEM_KEYS: Record<GeometryProblem, string> = {
  tooFewVertices: 'entities:geofenceErrTooFewVertices',
  tooManyPositions: 'entities:geofenceErrTooManyPositions',
  coordinateOutOfRange: 'entities:geofenceErrCoordinateRange',
  coordinateNotFinite: 'entities:geofenceErrCoordinateRange',
  repeatedVertex: 'entities:geofenceErrRepeatedVertex',
  selfIntersecting: 'entities:geofenceErrSelfIntersecting',
};

export function GeoFenceForm({
  entity,
  onDone,
}: {
  entity?: GeoFence;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const tenantBasemap = useTenantBasemap();
  const editing = entity != null;

  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const initialVertices = useMemo(
    () => (entity?.geometry ? (fromGeometryDocument(entity.geometry) ?? []) : []),
    [entity?.geometry],
  );
  const [vertices, setVertices] = useState<Vertex[]>(initialVertices);
  // An existing ring opens CLOSED. Otherwise the first click anywhere on the map
  // would append a corner to a finished fence, which is never what someone who
  // opened it to change its name was reaching for.
  const [closed, setClosed] = useState(initialVertices.length >= MIN_VERTICES);
  // 🔴 Two states per field, and the split is the point. The map is rebuilt from
  // scratch when its style changes, so driving it from the INPUT would tear it
  // down on every keystroke — forty rebuilds while typing a tile template, each
  // one firing real tile requests at a truncated URL and resetting the camera.
  // The draft follows the keyboard; the applied value moves only on blur.
  const [tileUrlDraft, setTileUrlDraft] = useState(() => readPreference(TILE_URL_KEY));
  const [tileUrl, setTileUrl] = useState(tileUrlDraft);
  const [attributionDraft, setAttributionDraft] = useState(() =>
    readPreference(TILE_ATTRIBUTION_KEY),
  );
  const [attribution, setAttribution] = useState(attributionDraft);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // 🔴 A fence whose geometry this editor cannot represent (a reserved kind, a
  // ring with holes) must NOT open as an empty map. This form always SENDS a
  // geometry — it is one of the three fields an edit names — so saving from a blank
  // canvas would overwrite the real shape with nothing. Refusing to edit is the only
  // safe reading of "cannot parse", and the partial update does not soften it: the
  // danger was never the fields the request omits, it is the one it names wrongly.
  const unreadable = editing && entity.geometry !== '' && fromGeometryDocument(entity.geometry) === null;

  /**
   * 🔴 A ring that falls below three corners cannot still be "finished", and the
   * reset is what keeps the editor escapable.
   *
   * While closed, a map click does not add a corner. Remove corners one by one
   * from a closed fence and, without this, the operator reaches an empty map with
   * clicks inert, Undo and Clear both disabled (nothing to undo), and no control
   * anywhere that reopens drawing — the form has to be abandoned. Reopening on
   * the way down is also what makes removeVertex's promise true: the ring really
   * is recoverable by placing another corner.
   */
  const applyVertices = (next: Vertex[]) => {
    setVertices(next);
    if (next.length < MIN_VERTICES) setClosed(false);
  };

  /** Undo and Clear are "keep drawing" actions, so they always reopen the ring. */
  const redraw = (next: Vertex[]) => {
    setVertices(next);
    setClosed(false);
  };

  const check = checkGeometry(vertices);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      // 🔴 AN EDIT SENDS EXACTLY WHAT THIS EDITOR EDITS, AND NOTHING ELSE. It used to
      // send the fence's whole stored state — metadata included — because the update was
      // a full replace and an omitted field was a deleted one. Under the partial update
      // an omitted field is left alone, so re-sending metadata would mean writing back a
      // value nobody looked at, over whatever it has become since this form opened.
      //
      // A CREATE still names everything, because a create has nothing to leave alone.
      const edited = {
        name: name.trim() || null,
        description: description.trim() || null,
        geometry: toGeometryDocument(vertices),
      };
      if (editing) {
        await updateGeoFence(entity.token, edited);
        onDone(t('entities:geofenceUpdatedToast', { token: entity.token }));
      } else {
        const created: Required<GeoFenceCreateRequest> = {
          token: token.trim(),
          metadata: null,
          ...edited,
        };
        await createGeoFence(created);
        onDone(t('entities:geofenceCreatedToast', { token: created.token }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  // Precedence: this browser's personal override → the tenant's basemap → nothing
  // (ADR-079 §9). 🔴 resolveBasemap is not a per-field fold: typing a tile URL here
  // does NOT keep the tenant's credit line underneath it, because that would credit
  // the tenant's provider for tiles it is not serving. It also does not render those
  // tiles bare — an override missing its credit line is discarded whole, and the
  // tenant's properly-credited basemap is drawn instead.
  const basemap = renderableBasemap(
    resolveBasemap({ tileUrl, attribution }, tenantBasemap),
  );
  // 🔴 …which would be SILENT without this. The rule is right and invisible: someone
  // pastes a tile URL, presses tab, and the map goes on showing the tenant's tiles
  // with no indication that what they typed was set aside. Saying so is the whole
  // difference between a licence rule and a bug report.
  const tileUrlUncredited = tileUrl.trim() !== '' && attribution.trim() === '';
  // The tenant's centre is a FALLBACK, used only when there is no ring to fit to. An
  // existing fence always wins — editing a fence in Rome from a tenant centred on
  // Atlanta would otherwise open on Atlanta every time.
  const initialView = vertices.length === 0 ? fallbackView(tenantBasemap) : null;

  const commitTileUrl = () => {
    setTileUrl(tileUrlDraft);
    writePreference(TILE_URL_KEY, tileUrlDraft);
  };
  const commitAttribution = () => {
    setAttribution(attributionDraft);
    writePreference(TILE_ATTRIBUTION_KEY, attributionDraft);
  };

  if (unreadable) {
    return (
      <ErrorBanner message={t('entities:geofenceUnreadableGeometry')} />
    );
  }

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}

      <FormField
        label={t('common:colToken')}
        htmlFor="gf-token"
        description={editing ? t('entities:geofenceTokenFixed') : undefined}
      >
        {editing ? (
          <Input id="gf-token" value={token} disabled />
        ) : (
          <TokenField
            id="gf-token"
            entityType="geofence"
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={t('entities:geofenceTokenPlaceholder')}
          />
        )}
      </FormField>

      <FormField label={t('common:colName')} htmlFor="gf-name">
        <Input id="gf-name" value={name} onChange={(ev) => setName(ev.target.value)} />
      </FormField>

      <FormField label={t('common:colDescription')} htmlFor="gf-description">
        <Textarea
          id="gf-description"
          value={description}
          onChange={(ev) => setDescription(ev.target.value)}
        />
      </FormField>

      {/* No htmlFor: `label[for]` must reference a LABELABLE element (input,
          select, textarea, button, …) and the drawing surface is a div. Pointing
          it at one is invalid HTML and associates nothing, which is what it was
          doing before. The map carries its own role="application" + aria-label,
          which is the correct accessible name for an interactive region. */}
      <FormField label={t('entities:geofenceBoundary')}>
        <div className="space-y-2">
          <FenceMap
            vertices={vertices}
            onChange={applyVertices}
            onClose={() => setClosed(true)}
            tileUrl={basemap?.tileUrl}
            attribution={basemap?.attribution ?? undefined}
            initialCenter={initialView?.center}
            initialZoom={initialView?.zoom ?? undefined}
            // 🔴 Two props, not one. Passing `busy || closed` as `disabled` — which
            // is what this did — froze every corner of an existing fence, because
            // a saved ring opens closed. Finished means "no new corners"; it must
            // never mean "not editable".
            closed={closed}
            disabled={busy}
          />
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-muted-foreground text-xs" data-testid="fence-vertex-count">
              {t('entities:geofenceVertexCount', {
                count: vertices.length,
                max: MAX_FENCE_VERTICES,
              })}
            </span>
            <span className="flex-1" />
            <Button
              variant="outline"
              size="sm"
              onClick={() => redraw(vertices.slice(0, -1))}
              disabled={busy || vertices.length === 0}
            >
              {t('entities:geofenceUndo')}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => redraw([])}
              disabled={busy || vertices.length === 0}
            >
              {t('entities:geofenceClear')}
            </Button>
          </div>
          {/* Only once there is something to edit: on an untouched map none of
              the three gestures it describes has a target. */}
          {vertices.length > 0 && (
            <p className="text-muted-foreground text-xs" data-testid="fence-edit-hint">
              {t('entities:geofenceEditHint')}
            </p>
          )}
          {closed && (
            <p className="text-muted-foreground text-xs" data-testid="fence-closed">
              {t('entities:geofenceClosedHint')}
            </p>
          )}
          {/* Shown only once something is drawn: "too few corners" on an
              untouched map describes an empty form, not a mistake. */}
          {vertices.length > 0 && check.problem && (
            <p className="text-destructive text-xs" data-testid="fence-geometry-problem">
              {t(PROBLEM_KEYS[check.problem], {
                count: vertices.length,
                max: MAX_FENCE_VERTICES,
              })}
            </p>
          )}
        </div>
      </FormField>

      <details className="rounded-md border px-3 py-2">
        <summary className="cursor-pointer text-sm">{t('entities:geofenceBasemap')}</summary>
        <div className="mt-3 space-y-3">
          <FormField
            label={t('entities:geofenceTileUrl')}
            htmlFor="gf-tile-url"
            description={t('entities:geofenceTileUrlHelp')}
          >
            <Input
              id="gf-tile-url"
              value={tileUrlDraft}
              onChange={(ev) => setTileUrlDraft(ev.target.value)}
              onBlur={commitTileUrl}
              placeholder={t('entities:geofenceTileUrlPlaceholder')}
            />
          </FormField>
          <FormField
            label={t('entities:geofenceTileAttribution')}
            htmlFor="gf-tile-attribution"
            description={t('entities:geofenceTileAttributionHelp')}
          >
            <Input
              id="gf-tile-attribution"
              value={attributionDraft}
              onChange={(ev) => setAttributionDraft(ev.target.value)}
              onBlur={commitAttribution}
            />
          </FormField>
          {tileUrlUncredited && (
            <p className="text-destructive text-xs" data-testid="geofence-tile-uncredited">
              {t('entities:geofenceTileUncredited')}
            </p>
          )}
        </div>
      </details>

      <div className="flex gap-2">
        <Button
          onClick={submit}
          loading={busy}
          disabled={busy || !check.ok || (!editing && !token.trim())}
        >
          {editing ? t('common:saveChanges') : t('entities:geofenceCreateAction')}
        </Button>
      </div>
    </div>
  );
}
