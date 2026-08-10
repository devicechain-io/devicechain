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
import { Textarea, errMessage } from '@/routes/common';
import {
  createGeoFence,
  updateGeoFence,
  type GeoFence,
  type GeoFenceCreateRequest,
} from '@/lib/api/geofences';
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

// The basemap is a per-BROWSER preference, not a tenant setting and not a
// per-fence field. There is deliberately no platform default tile source —
// shipping one would point every self-hosted instance at a public tile service
// that never agreed to serve it — so until an instance-level default exists, the
// operator supplies their own and it is remembered here.
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
  // ring with holes) must NOT open as an empty map. updateGeoFence is a full
  // replace, so saving from a blank canvas would overwrite the real geometry
  // with nothing. Refusing to edit is the only safe reading of "cannot parse".
  const unreadable = editing && entity.geometry !== '' && fromGeometryDocument(entity.geometry) === null;

  const check = checkGeometry(vertices);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const request: GeoFenceCreateRequest = {
        token: editing ? entity.token : token.trim(),
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        geometry: toGeometryDocument(vertices),
        // 🔴 Carried through UNCHANGED rather than omitted. updateGeoFence
        // replaces the whole record, so a request that leaves metadata out
        // silently deletes whatever an operator or an integration had set.
        metadata: entity?.metadata ?? undefined,
      };
      if (editing) {
        await updateGeoFence(entity.token, request);
        onDone(t('entities:geofenceUpdatedToast', { token: entity.token }));
      } else {
        await createGeoFence(request);
        onDone(t('entities:geofenceCreatedToast', { token: request.token }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

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
            onChange={setVertices}
            onClose={() => setClosed(true)}
            tileUrl={tileUrl || undefined}
            attribution={attribution || undefined}
            disabled={busy || closed}
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
              onClick={() => {
                setVertices(vertices.slice(0, -1));
                setClosed(false);
              }}
              disabled={busy || vertices.length === 0}
            >
              {t('entities:geofenceUndo')}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setVertices([]);
                setClosed(false);
              }}
              disabled={busy || vertices.length === 0}
            >
              {t('entities:geofenceClear')}
            </Button>
          </div>
          {/* The problem is shown only once something is drawn: "too few
              vertices" on an untouched map is a description of an empty form,
              not a mistake the operator made. */}
          {closed && (
            <p className="text-muted-foreground text-xs" data-testid="fence-closed">
              {t('entities:geofenceClosedHint')}
            </p>
          )}
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
