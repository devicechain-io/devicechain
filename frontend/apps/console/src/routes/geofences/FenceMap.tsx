// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The drawing surface. A controlled component: it owns no ring state, only the
// map instance and the handlers that turn clicks into vertices. Every rule about
// what a ring may be lives in geometry.ts, where it can be tested without a DOM.
//
// 🔴 MapLibre is loaded ONLY through the dynamic import below. A static import
// anywhere in this file folds the whole renderer — several hundred KB — into the
// console's entry chunk for every operator, including everyone who never opens
// this page. bundle-boundaries.test.ts is the gate that keeps it honest.

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { rasterStyleFor } from '@devicechain/widgets';
import type { Map as MapLibreMap, MapMouseEvent } from 'maplibre-gl';
import { boundsOf, isCloseGesture, toVertex, type Vertex } from './geometry';

/// <reference path="../../../../../packages/widgets/src/css-modules.d.ts" />

let mapLibrePromise: Promise<typeof import('maplibre-gl')> | undefined;

function loadMapLibre(): Promise<typeof import('maplibre-gl')> {
  if (!mapLibrePromise) {
    mapLibrePromise = Promise.all([
      import('maplibre-gl'),
      import('maplibre-gl/dist/maplibre-gl.css'),
    ]).then(([mod]) => {
      // Same UMD-interop shim the dashboard map widget carries: some bundlers
      // hand back the namespace, others the default export.
      const lib = mod as unknown as Record<string, unknown>;
      return typeof lib.Map === 'function'
        ? mod
        : (lib.default as typeof import('maplibre-gl'));
    });
  }
  return mapLibrePromise;
}

const SOURCE = 'fence';
const FILL_LAYER = 'fence-fill';
const LINE_LAYER = 'fence-outline';
const VERTEX_LAYER = 'fence-vertices';

/**
 * The style used when no tile URL is configured. MapLibre is still what renders
 * it — unlike the dashboard's map widget, which falls back to a static projected
 * panel when it has no tiles.
 *
 * The difference is deliberate: a VIEWER without a basemap can be shown a
 * flat approximation, but an AUTHOR needs the real projection, because the
 * coordinates a click produces are the ones that get stored. Drawing on an
 * approximation and saving through a different projection would put the fence
 * somewhere the operator never pointed at.
 */
function blankStyle(): unknown {
  return {
    version: 8,
    sources: {},
    layers: [{ id: 'background', type: 'background', paint: { 'background-color': '#0b1220' } }],
  };
}

function ringFeature(vertices: readonly Vertex[]) {
  // A polygon needs a closed ring to render a fill; while drawing there are
  // often too few vertices for one, so the fill is fed a closed copy and the
  // outline is fed the open path. Feeding both from one geometry would either
  // hide the fill until the third click or draw a phantom closing edge from the
  // first click onward.
  const path = vertices.map((v) => [v.lng, v.lat]);
  const closed = path.length >= 3 ? [...path, path[0]] : [];
  return {
    type: 'FeatureCollection',
    features: [
      {
        type: 'Feature',
        properties: { part: 'fill' },
        geometry: { type: 'Polygon', coordinates: closed.length ? [closed] : [] },
      },
      {
        type: 'Feature',
        properties: { part: 'outline' },
        geometry: { type: 'LineString', coordinates: path },
      },
      ...vertices.map((v, i) => ({
        type: 'Feature',
        properties: { part: 'vertex', index: i },
        geometry: { type: 'Point', coordinates: [v.lng, v.lat] },
      })),
    ],
  };
}

export interface FenceMapProps {
  vertices: Vertex[];
  onChange: (vertices: Vertex[]) => void;
  /** Raster tile template. Absent means draw against a blank background. */
  tileUrl?: string;
  attribution?: string;
  /** Fires when the operator closes the ring by clicking the first vertex. */
  onClose?: () => void;
  disabled?: boolean;
}

export function FenceMap({
  vertices,
  onChange,
  tileUrl,
  attribution,
  onClose,
  disabled = false,
}: FenceMapProps) {
  const { t } = useTranslation(['entities', 'common']);
  const container = useRef<HTMLDivElement | null>(null);
  const map = useRef<MapLibreMap | null>(null);
  const [failed, setFailed] = useState(false);

  // The click handler is registered once, but needs the CURRENT ring and
  // callbacks. Reading them from refs keeps the handler stable, so a vertex
  // added mid-draw cannot detach the listener that added it.
  const live = useRef({ vertices, onChange, onClose, disabled });
  // Assigned in a layout effect rather than during render: a render React throws
  // away must not leave its props behind for a click handler to read. Nothing in
  // the console suspends above this form today, so the difference is currently
  // unobservable — but that is a property of the CALLER, and this component
  // should not depend on it.
  useLayoutEffect(() => {
    live.current = { vertices, onChange, onClose, disabled };
  });

  const handleClick = useCallback((event: MapMouseEvent) => {
    const { vertices: current, onChange: emit, onClose: close, disabled: off } = live.current;
    if (off) return;

    // 🔴 wrap() is belt to renderWorldCopies:false's braces. maplibre derives
    // lngLat from an unwrapped mercator inverse, so any path that lets the view
    // run past the antimeridian hands back a longitude outside [-180, 180]. Left
    // unwrapped it becomes a corner the form rejects as out of range — blaming
    // the operator for a click on land the map itself drew.
    const { lng, lat } = event.lngLat.wrap();

    // Closing the ring is a click ON the first vertex. The rule itself lives in
    // geometry.ts so it can be tested without a map; all this does is supply the
    // projection, which is the one part that genuinely needs MapLibre.
    if (map.current && current.length > 0) {
      // Unambiguous because renderWorldCopies is off (see the Map options): with
      // one world on screen there is exactly one pixel position for a lng/lat, so
      // this cannot compare a stored vertex against a click on a neighbouring copy.
      const first = map.current.project([current[0].lng, current[0].lat]);
      if (isCloseGesture(current.length, first, event.point)) {
        close?.();
        return;
      }
    }

    emit([...current, toVertex(lng, lat)]);
  }, []);

  // ── Map lifecycle: created once, never recreated for a vertex change ──
  useEffect(() => {
    let cancelled = false;
    const node = container.current;
    if (!node) return;

    loadMapLibre()
      .then((maplibre) => {
        if (cancelled || !container.current) return;
        const instance = new maplibre.Map({
          container: container.current,
          style: (tileUrl ? rasterStyleFor(tileUrl, attribution) : blankStyle()) as never,
          center: [0, 20],
          zoom: 1.5,
          // 🔴 No repeated worlds. With copies on (the default) the world repeats
          // across a wide viewport at this zoom, and a click on a copy returns an
          // unwrapped longitude — 372.49 for Rome one world over — which the ring
          // would then carry as an out-of-range corner. It also makes project()
          // ambiguous for the close gesture. A fence is site-scale; seeing three
          // Earths buys an author nothing and costs both of those.
          renderWorldCopies: false,
          attributionControl: tileUrl ? undefined : false,
        });
        instance.addControl(new maplibre.NavigationControl({ showCompass: false }), 'top-right');
        instance.on('load', () => {
          if (cancelled) return;
          instance.addSource(SOURCE, {
            type: 'geojson',
            data: ringFeature(live.current.vertices) as never,
          });
          instance.addLayer({
            id: FILL_LAYER,
            type: 'fill',
            source: SOURCE,
            filter: ['==', ['get', 'part'], 'fill'],
            paint: { 'fill-color': '#38bdf8', 'fill-opacity': 0.2 },
          });
          instance.addLayer({
            id: LINE_LAYER,
            type: 'line',
            source: SOURCE,
            filter: ['==', ['get', 'part'], 'outline'],
            paint: { 'line-color': '#38bdf8', 'line-width': 2 },
          });
          instance.addLayer({
            id: VERTEX_LAYER,
            type: 'circle',
            source: SOURCE,
            filter: ['==', ['get', 'part'], 'vertex'],
            paint: {
              'circle-radius': 5,
              'circle-color': '#f8fafc',
              'circle-stroke-color': '#0284c7',
              'circle-stroke-width': 2,
            },
          });

          const bounds = boundsOf(live.current.vertices);
          if (bounds) instance.fitBounds(bounds, { padding: 48, animate: false, maxZoom: 17 });
        });
        instance.on('click', handleClick);
        map.current = instance;
      })
      .catch(() => {
        // 🔴 STATE, not a ref. This was a ref that nothing read, under a comment
        // claiming the surface would say so — which made the failure exactly the
        // silent empty box the comment said it prevented. A ref write cannot
        // re-render, so the message could never have appeared.
        if (!cancelled) setFailed(true);
      });

    return () => {
      cancelled = true;
      map.current?.remove();
      map.current = null;
    };
    // Rebuilding the map when the tile URL changes is intended — a style swap
    // mid-draw is rarer and less surprising than a stale basemap.
  }, [tileUrl, attribution, handleClick]);

  // ── Ring updates: push into the existing source, never rebuild the map ──
  useEffect(() => {
    const instance = map.current;
    if (!instance) return;
    const source = instance.getSource(SOURCE) as { setData?: (d: unknown) => void } | undefined;
    source?.setData?.(ringFeature(vertices));
  }, [vertices]);

  const cursor = disabled ? 'default' : 'crosshair';

  return (
    <div className="relative">
      <div
        ref={container}
        data-testid="fence-map"
        className="h-[380px] w-full overflow-hidden rounded-md border bg-muted"
        style={{ cursor }}
        role="application"
        aria-label={t('entities:geofenceMapLabel')}
      />
      {failed && (
        <p className="text-destructive mt-2 text-xs" data-testid="fence-map-failed">
          {t('entities:geofenceMapUnavailable')}
        </p>
      )}
      {!failed && !tileUrl && (
        <p className="text-muted-foreground mt-2 text-xs">{t('entities:geofenceNoTilesHint')}</p>
      )}
    </div>
  );
}
