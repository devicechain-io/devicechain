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
//
// 🔴 MAPLIBRE RENDERS THIS EDITOR WHETHER OR NOT A TILE SOURCE IS CONFIGURED, and
// that is a correctness requirement rather than a preference. An AUTHOR needs the
// REAL projection: the coordinates a click produces here are the ones that get
// stored, so drawing on a flat approximation and saving through a different
// projection would put the fence somewhere the operator never pointed at. The
// unconfigured branch swaps the STYLE — the bundled Natural Earth world, in place
// of what used to be an empty void — and touches nothing about the projection.
// map-geometry.ts declares mercator on both styles for exactly this reason.

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { loadLandStyle, rasterStyleFor } from '@devicechain/widgets';
import type { Map as MapLibreMap, MapMouseEvent } from 'maplibre-gl';

import { MAP_RUNTIME } from '@/map-runtime';
import {
  DRAG_THRESHOLD_PX,
  boundsOf,
  clickIntent,
  edgeMidpoints,
  hasMovedEnough,
  insertVertexOnEdge,
  moveVertex,
  removeVertex,
  toVertex,
  type Vertex,
} from './geometry';

let mapLibrePromise: Promise<typeof import('maplibre-gl')> | undefined;

// The fence editor drives its OWN MapLibre instance — a fence is drawn, not rendered from a
// dashboard definition — so it needs the same bootstrap the map widget needs: MapLibre
// derives its worker URL at runtime from its own module URL, which no bundler can follow,
// so without `setWorkerUrl` the file is never emitted and the map renders nothing while
// every gate stays green.
//
// 🔴 IT TAKES THE URL FROM THE APP'S ONE RUNTIME rather than importing the worker entry
// again. Two copies of a bundler incantation in one app is one copy too many, and when they
// drift the symptom is a blank map on one surface only — the hardest version of this bug to
// find. `?worker&url` is written once, in @devicechain/widgets/vite, and reaches here
// through src/map-runtime.ts.
function loadMapLibre(): Promise<typeof import('maplibre-gl')> {
  if (!mapLibrePromise) {
    mapLibrePromise = Promise.all([
      import('maplibre-gl'),
      MAP_RUNTIME.loadStyles?.() ?? Promise.resolve(),
    ]).then(([mod]) => {
      mod.setWorkerUrl(MAP_RUNTIME.workerUrl);
      return mod;
    });
  }
  return mapLibrePromise;
}

const SOURCE = 'fence';
const FILL_LAYER = 'fence-fill';
const LINE_LAYER = 'fence-outline';
const VERTEX_LAYER = 'fence-vertices';
const MIDPOINT_LAYER = 'fence-midpoints';
const GHOST_LAYER = 'fence-ghost';

function ringFeature(vertices: readonly Vertex[], ghost: readonly Vertex[] = []) {
  // A polygon needs a closed ring to render a fill; while drawing there are
  // often too few vertices for one, so the fill is fed a closed copy and the
  // outline is fed the open path. Feeding both from one geometry would either
  // hide the fill until the third click or draw a phantom closing edge from the
  // first click onward.
  const path = vertices.map((v) => [v.lng, v.lat]);
  const closed = path.length >= 3 ? [...path, path[0]] : [];
  // The comparison ring, drawn as an outline only so the two never read as one
  // filled shape. It carries no vertex or midpoint handles: it is a picture of
  // another version, not something that can be edited.
  const ghostPath = ghost.map((v) => [v.lng, v.lat]);
  const ghostClosed = ghostPath.length >= 3 ? [...ghostPath, ghostPath[0]] : ghostPath;

  return {
    type: 'FeatureCollection',
    features: [
      {
        type: 'Feature',
        properties: { part: 'ghost' },
        geometry: { type: 'LineString', coordinates: ghostClosed },
      },
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
      // One handle per EDGE, at its midpoint, carrying the index of the edge it
      // splits — not the index of a vertex. Clicking one adds a corner there.
      // Only shown once the ring encloses something; on two corners the "edges"
      // are the same segment twice and the two handles would sit on top of each
      // other, each claiming a different edge.
      ...(vertices.length >= 3
        ? edgeMidpoints(vertices).map((m, i) => ({
            type: 'Feature',
            properties: { part: 'midpoint', edge: i },
            geometry: { type: 'Point', coordinates: [m.lng, m.lat] },
          }))
        : []),
    ],
  };
}

export interface FenceMapProps {
  vertices: Vertex[];
  onChange: (vertices: Vertex[]) => void;
  /**
   * Raster tile template. Absent means draw against the BUNDLED world basemap —
   * public-domain Natural Earth land and country outlines, compiled into the app.
   * It is schematic and stops being useful long before street zoom, which is the
   * honest signal to configure a provider; what it is not is the empty void this
   * used to render, which was indistinguishable from a broken editor.
   */
  tileUrl?: string;
  attribution?: string;
  /** Fires when the operator closes the ring by clicking the first vertex. */
  onClose?: () => void;
  /**
   * The ring is finished: map clicks no longer APPEND a corner.
   *
   * 🔴 Distinct from `disabled` on purpose. In the first version there was only
   * `disabled`, and the form passed `busy || closed` into it — which meant an
   * EXISTING fence, which opens closed, could not be edited at all. Every corner
   * was frozen and the only way to change a saved shape was to clear it and draw
   * again. Finished must mean "no new corners", never "no editing".
   */
  closed?: boolean;
  /** Saving is in flight: nothing responds. */
  disabled?: boolean;
  /**
   * Where the camera opens, as MapLibre's [longitude, latitude], when there is
   * nothing to fit to (ADR-079).
   *
   * 🔴 A FALLBACK, not an override. An existing ring's bounds always win — the fit
   * below runs after this and supersedes it — so a tenant centred on Atlanta never
   * drags an author editing a fence in Rome back across the Atlantic. Absent keeps
   * the whole-world view, which is the right answer when nobody has said otherwise.
   *
   * 🔴 Read ONLY at construction, through a ref, and deliberately absent from the
   * lifecycle effect's dependencies. Both alternatives are worse: a fresh
   * `[lng, lat]` array in the deps rebuilds the map on every render, and depending on
   * it "correctly" would tear the map down mid-draw the moment the fallback stops
   * applying — it goes null as soon as the first corner is placed. The map already
   * rebuilds when the tile URL changes, which is when a late-arriving tenant basemap
   * lands, so the ordinary path picks this up anyway.
   */
  initialCenter?: [number, number];
  /** Zoom to pair with initialCenter. Ignored without one. */
  initialZoom?: number;
  /**
   * A second ring drawn as a muted outline for comparison — the same fence at
   * another version. Never editable, and deliberately outline-only: two filled
   * shapes read as one.
   */
  ghost?: Vertex[];
}

export function FenceMap({
  vertices,
  onChange,
  tileUrl,
  attribution,
  onClose,
  closed = false,
  disabled = false,
  initialCenter,
  initialZoom,
  ghost,
}: FenceMapProps) {
  const { t } = useTranslation(['entities', 'common']);
  const container = useRef<HTMLDivElement | null>(null);
  const map = useRef<MapLibreMap | null>(null);
  const [failed, setFailed] = useState(false);

  // The click handler is registered once, but needs the CURRENT ring and
  // callbacks. Reading them from refs keeps the handler stable, so a vertex
  // added mid-draw cannot detach the listener that added it.
  const live = useRef({ vertices, onChange, onClose, closed, disabled, ghost });
  // The opening camera, read once at construction — see initialCenter's note above.
  const initialView = useRef({ center: initialCenter, zoom: initialZoom });
  initialView.current = { center: initialCenter, zoom: initialZoom };
  /**
   * The pointer gesture in progress on a vertex, if any.
   *
   * 🔴 `moved` is the load-bearing field, and its absence broke the close gesture
   * outright. Without a movement threshold, a plain CLICK on a corner entered the
   * drag machinery, which then swallowed the click that followed — so clicking
   * the first corner, the one gesture the map's own instructions describe, did
   * nothing at all unless the operator MISSED the dot by a few pixels and landed
   * in the gap between it and the close radius.
   */
  const drag = useRef<{ index: number; startX: number; startY: number; moved: boolean } | null>(
    null,
  );
  /** A drag that actually moved just ended; its trailing click is not a click. */
  const justDragged = useRef(false);
  /**
   * A layer-scoped handler has already acted on the click being processed.
   *
   * Layer handlers are registered BEFORE the general one so they run first and
   * can say so. An earlier version registered them after and set a
   * "swallow the NEXT click" flag instead, which could not protect the current
   * click and instead ate the operator's following one — every alt-removal and
   * every edge insert silently cost the next click.
   */
  const consumedByLayer = useRef(false);
  // Assigned in a layout effect rather than during render: a render React throws
  // away must not leave its props behind for a click handler to read. Nothing in
  // the console suspends above this form today, so the difference is currently
  // unobservable — but that is a property of the CALLER, and this component
  // should not depend on it.
  useLayoutEffect(() => {
    live.current = { vertices, onChange, onClose, closed, disabled, ghost };
  });

  const handleClick = useCallback((event: MapMouseEvent) => {
    const { vertices: current, onChange: emit, onClose: close, closed: done, disabled: off } =
      live.current;
    if (off) return;

    const instance = map.current;
    // 🔴 wrap() is belt to renderWorldCopies:false's braces. maplibre derives
    // lngLat from an unwrapped mercator inverse, so any path that lets the view
    // run past the antimeridian hands back a longitude outside [-180, 180]. Left
    // unwrapped it becomes a corner the form rejects as out of range — blaming
    // the operator for a click on land the map itself drew.
    const { lng, lat } = event.lngLat.wrap();

    // Projecting the first vertex is the only part of the decision that needs
    // MapLibre. WHAT the click means is decided by clickIntent, which lives in
    // geometry.ts and is tested there — because that decision broke once while it
    // lived here, where nothing could see it.
    //
    // Unambiguous because renderWorldCopies is off: with one world on screen
    // there is exactly one pixel position for a lng/lat.
    const firstVertexPoint =
      instance && current.length > 0
        ? instance.project([current[0].lng, current[0].lat])
        : null;

    const intent = clickIntent({
      justDragged: justDragged.current,
      consumedByLayer: consumedByLayer.current,
      closed: done,
      vertices: current,
      firstVertexPoint,
      clickPoint: event.point,
    });

    // Cleared here rather than where they are set: this runs last, so it is the
    // only place that knows the click is over.
    justDragged.current = false;
    consumedByLayer.current = false;

    if (intent === 'close') {
      close?.();
      return;
    }
    if (intent === 'append') emit([...current, toVertex(lng, lat)]);
  }, []);

  /** Which vertex, if any, is under the pointer — null before the layers exist. */
  const vertexIndexAt = (instance: MapLibreMap, event: MapMouseEvent): number | null => {
    if (!instance.getLayer(VERTEX_LAYER)) return null;
    const hits = instance.queryRenderedFeatures(event.point, { layers: [VERTEX_LAYER] });
    const index = hits[0]?.properties?.index;
    return typeof index === 'number' ? index : null;
  };

  /**
   * Any new pointer gesture clears the leftovers of the previous one.
   *
   * A drag released off-canvas produces no click, so a flag set at mouseup would
   * survive to eat an unrelated click later — invisibly, and possibly the very
   * click the operator meant as "close the ring". Every gesture starts with a
   * mousedown, so clearing here bounds staleness to nothing.
   */
  const handleMapMouseDown = useCallback(() => {
    justDragged.current = false;
    consumedByLayer.current = false;
  }, []);

  const handleVertexMouseDown = useCallback((event: MapMouseEvent) => {
    if (live.current.disabled) return;
    // Left button only. Right-clicking a corner otherwise ran the whole drag
    // machinery while also opening the context menu.
    if (event.originalEvent.button !== 0) return;
    const instance = map.current;
    if (!instance) return;
    const index = vertexIndexAt(instance, event);
    if (index === null) return;
    // Take the gesture from the map now, not at the movement threshold: by the
    // time the threshold is crossed the map would already have panned.
    event.preventDefault();
    instance.dragPan.disable();
    drag.current = { index, startX: event.point.x, startY: event.point.y, moved: false };
  }, []);

  const handleMouseMove = useCallback((event: MapMouseEvent) => {
    const instance = map.current;
    if (!instance) return;
    const active = drag.current;

    if (!active) {
      // Not dragging: advertise what is grabbable. Guarded on the layers
      // existing — before `load` this query fires a MapLibre ErrorEvent on every
      // mousemove, which the default handler logs.
      if (live.current.disabled) {
        instance.getCanvas().style.cursor = '';
        return;
      }
      const ready = instance.getLayer(VERTEX_LAYER) && instance.getLayer(MIDPOINT_LAYER);
      const over =
        ready && instance.queryRenderedFeatures(event.point, {
          layers: [VERTEX_LAYER, MIDPOINT_LAYER],
        }).length > 0;
      instance.getCanvas().style.cursor = over ? 'grab' : live.current.closed ? '' : 'crosshair';
      return;
    }

    if (!active.moved) {
      if (!hasMovedEnough({ x: active.startX, y: active.startY }, event.point, DRAG_THRESHOLD_PX)) {
        return; // still a click, as far as anyone knows
      }
      active.moved = true;
      instance.getCanvas().style.cursor = 'grabbing';
    }

    const { lng, lat } = event.lngLat.wrap();
    live.current.onChange(moveVertex(live.current.vertices, active.index, toVertex(lng, lat)));
  }, []);

  const handleMouseUp = useCallback(() => {
    const active = drag.current;
    if (!active) return;
    drag.current = null;
    // Only a gesture that MOVED produces a trailing click worth ignoring. A press
    // that never crossed the threshold is a click, and must be left to mean
    // whatever a click on that corner means — including closing the ring.
    justDragged.current = active.moved;
    const instance = map.current;
    if (instance) {
      instance.dragPan.enable();
      instance.getCanvas().style.cursor = '';
    }
  }, []);

  const handleVertexClick = useCallback((event: MapMouseEvent) => {
    const { disabled: off, vertices: current, onChange: emit } = live.current;
    if (off || justDragged.current) return;
    const instance = map.current;
    if (!instance) return;
    // Alt/Option removes. A modifier rather than a plain click because a plain
    // click on the FIRST corner already means "close the ring", and one gesture
    // cannot mean two things.
    if (!event.originalEvent.altKey) return;
    const index = vertexIndexAt(instance, event);
    if (index === null) return;
    consumedByLayer.current = true;
    emit(removeVertex(current, index));
  }, []);

  const handleMidpointClick = useCallback((event: MapMouseEvent) => {
    const { disabled: off, vertices: current, onChange: emit } = live.current;
    if (off || justDragged.current) return;
    const instance = map.current;
    if (!instance || !instance.getLayer(MIDPOINT_LAYER)) return;
    const hits = instance.queryRenderedFeatures(event.point, { layers: [MIDPOINT_LAYER] });
    const edge = hits[0]?.properties?.edge;
    if (typeof edge !== 'number') return;
    const { lng, lat } = event.lngLat.wrap();
    consumedByLayer.current = true;
    emit(insertVertexOnEdge(current, edge, toVertex(lng, lat)));
  }, []);

  // ── Map lifecycle: created once, never recreated for a vertex change ──
  useEffect(() => {
    let cancelled = false;
    const node = container.current;
    if (!node) return;

    // The style is resolved alongside the renderer rather than inline, because the
    // unconfigured one now has to be FETCHED — the bundled world geometry lives
    // behind its own dynamic import so a configured instance never downloads it.
    // Both arrive before the map is constructed, so there is still exactly one
    // Map, built once, with its final style; nothing re-styles after the fact.
    Promise.all([
      loadMapLibre(),
      tileUrl ? rasterStyleFor(tileUrl, attribution) : loadLandStyle(),
    ])
      .then(([maplibre, style]) => {
        if (cancelled || !container.current) return;
        const instance = new maplibre.Map({
          container: container.current,
          style: style as never,
          // The whole-world view is the floor: it is what "nobody has told us where
          // this tenant works" should look like, rather than a guess at a country.
          center: initialView.current.center ?? [0, 20],
          zoom: initialView.current.center ? (initialView.current.zoom ?? 12) : 1.5,
          // 🔴 No repeated worlds. With copies on (the default) the world repeats
          // across a wide viewport at this zoom, and a click on a copy returns an
          // unwrapped longitude — 372.49 for Rome one world over — which the ring
          // would then carry as an out-of-range corner. It also makes project()
          // ambiguous for the close gesture. A fence is site-scale; seeing three
          // Earths buys an author nothing and costs both of those.
          renderWorldCopies: false,
          // 🔴 attributionControl IS DELIBERATELY ABSENT, AND REMOVING IT FIXED A
          // LICENCE BUG. The line here used to read:
          //
          //     attributionControl: tileUrl ? undefined : false
          //
          // which reads as "default when configured, off when not" and is not what
          // it does. MapLibre resolves options as `{...defaultOptions, ...options}`,
          // and a spread copies an explicitly-undefined property OVER the default —
          // so the key ended up undefined, `if (resolvedOptions.attributionControl)`
          // was falsy, and the control was suppressed in BOTH branches.
          //
          // The consequence was not cosmetic: the fence editor has never shown its
          // tile provider's credit line. For OpenStreetMap — now the shipped default
          // — displaying that credit is a term of their tile usage policy, so an
          // operator drawing a fence was looking at uncredited tiles.
          //
          // Omitting the key entirely is what actually gets the default. Both
          // branches now show their credit: the provider's for tiles, "Made with
          // Natural Earth" for the bundled world.
        });
        instance.addControl(new maplibre.NavigationControl({ showCompass: false }), 'top-right');
        instance.on('load', () => {
          if (cancelled) return;
          instance.addSource(SOURCE, {
            type: 'geojson',
            data: ringFeature(live.current.vertices, live.current.ghost) as never,
          });
          instance.addLayer({
            id: GHOST_LAYER,
            type: 'line',
            source: SOURCE,
            filter: ['==', ['get', 'part'], 'ghost'],
            paint: {
              'line-color': '#f59e0b',
              'line-width': 2,
              'line-dasharray': [2, 2],
              'line-opacity': 0.9,
            },
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
            id: MIDPOINT_LAYER,
            type: 'circle',
            source: SOURCE,
            filter: ['==', ['get', 'part'], 'midpoint'],
            paint: {
              'circle-radius': 4,
              'circle-color': '#0ea5e9',
              'circle-opacity': 0.55,
              'circle-stroke-color': '#f8fafc',
              'circle-stroke-width': 1,
            },
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

          const bounds = boundsOf([...live.current.vertices, ...(live.current.ghost ?? [])]);
          if (bounds) instance.fitBounds(bounds, { padding: 48, animate: false, maxZoom: 17 });
        });
        // 🔴 ORDER IS LOAD-BEARING: MapLibre invokes listeners in REGISTRATION
        // order, so the layer-scoped click handlers must be registered BEFORE the
        // general one. That is what lets a handle answer the click and tell the
        // general handler, via consumedByLayer, not to answer it again. Registered
        // the other way round — which is how this first shipped — the general
        // handler runs first, appends a corner for a click meant as an edge split,
        // and the flag can only poison the operator's NEXT click.
        instance.on('click', VERTEX_LAYER, handleVertexClick);
        instance.on('click', MIDPOINT_LAYER, handleMidpointClick);
        instance.on('click', handleClick);
        instance.on('mousedown', VERTEX_LAYER, handleVertexMouseDown);
        instance.on('mousedown', handleMapMouseDown);
        instance.on('mousemove', handleMouseMove);
        // On the WINDOW as well as the map, so a drag released outside the canvas
        // still ends instead of gluing the pointer to a corner it no longer owns.
        // Both fire for an on-canvas release; handleMouseUp is idempotent.
        //
        // 🔴 Not a complete guarantee, and the earlier wording claimed it was:
        // releasing outside the BROWSER WINDOW delivers no mouseup at all, so the
        // corner keeps following the pointer until the next press. Pointer capture
        // would close that; it is not wired here.
        instance.on('mouseup', handleMouseUp);
        window.addEventListener('mouseup', handleMouseUp);
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
      window.removeEventListener('mouseup', handleMouseUp);
      map.current?.remove();
      map.current = null;
    };
    // Rebuilding the map when the tile URL changes is intended — a style swap
    // mid-draw is rarer and less surprising than a stale basemap.
  }, [
    tileUrl,
    attribution,
    handleClick,
    handleMapMouseDown,
    handleVertexMouseDown,
    handleVertexClick,
    handleMidpointClick,
    handleMouseMove,
    handleMouseUp,
  ]);

  // ── Ring updates: push into the existing source, never rebuild the map ──
  useEffect(() => {
    const instance = map.current;
    if (!instance) return;
    const source = instance.getSource(SOURCE) as { setData?: (d: unknown) => void } | undefined;
    source?.setData?.(ringFeature(vertices, ghost));
  }, [vertices, ghost]);

  return (
    <div className="relative">
      <div
        ref={container}
        data-testid="fence-map"
        className="h-[380px] w-full overflow-hidden rounded-md border bg-muted"
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
