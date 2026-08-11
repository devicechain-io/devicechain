// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// map — the bound devices' last-known positions, on the location channel.
//
// Three rules shape everything here, and each of them is a state some other design
// would have collapsed into "the map is empty":
//
// 🔴 REFUSED IS NOT EMPTY. Position is gated on its own `location:read` authority,
// deliberately absent from the read-only viewer baseline, so an ordinary member with
// full telemetry access is routinely refused. "No devices to show" and "you are not
// allowed to see this" are opposite facts and only one of them is actionable, so the
// refusal gets its own copy and its own look — never a blank map.
//
// 🔴 NO TILE SOURCE IS NOT A BROKEN MAP. This package hardcodes no tile URL (see
// rasterStyleFor) and inherits one from the tenant (ADR-079), whose instance-wide
// default now ships a real provider — so in practice this branch is rare. It is not
// dead, though: an operator can empty that default, an air-gapped install can have no
// route to any provider at all, and a viewer can be looking at a board on either.
//
// That case renders a REAL MapLibre map against the BUNDLED public-domain world
// (loadLandStyle) — continents and country outlines, compiled into this package.
//
// 🔴 Be precise about what survives of the old promise, because an earlier version of
// this comment was not. It said the widget "issues no request to any host" and backed
// that with "loads no map library", and the second half is now FALSE: this path loads
// the renderer and the geometry, two chunks, from the app's OWN origin. What is still
// true — and is the part that ever mattered — is that it contacts no THIRD-PARTY host,
// because it has been given none it would be entitled to ask. What it no longer does
// is show a void that reads as breakage.
//
// 🔴 ABSENT IS NOT ZERO. A marker describes only what its device reported; an
// unreported heading is not due north. See describePosition.

/// <reference path="../css-modules.d.ts" />

import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { renderableBasemap, resolveBasemap } from '@devicechain/client';

import { useTenantBasemap } from '../basemap-context';
import { WidgetFrame } from '../frame';
import type { LocationStreamState } from '../hooks';
import { css } from '../theme';
import { optString, type WidgetProps } from '../widget';
import {
  describePosition,
  loadLandStyle,
  placeable,
  projectToPanel,
  rasterStyleFor,
  type PlaceableLocation,
} from './map-geometry';

// The MapLibre pieces this widget drives. A TYPE-only import is erased at build time,
// so naming them here creates no static dependency on the library — the value only ever
// arrives through the dynamic import below.
import type { Map as MapLibreMap, Marker as MapLibreMarker } from 'maplibre-gl';

// 🔴 THE LIBRARY IS LOADED LAZILY, BY CONSTRUCTION. MapLibre is ~200 KB gzipped, and
// only a dashboard that actually contains a map widget may pay for it — a console user
// who never opens a board with a map must not download a map renderer. The dynamic
// import is what puts it in its own chunk; a top-level `import 'maplibre-gl'` anywhere
// in this package would fold it into the main bundle for every viewer, and nothing
// about the widget's behaviour would change to reveal it. The stylesheet rides the same
// dynamic boundary for the same reason.
//
// Loaded once and memoized: several map widgets on one board share the fetch.
let mapLibrePromise: Promise<typeof import('maplibre-gl')> | undefined;

function loadMapLibre(): Promise<typeof import('maplibre-gl')> {
  if (!mapLibrePromise) {
    mapLibrePromise = Promise.all([
      import('maplibre-gl'),
      // 🔴 THE WORKER HAS TO BE ASKED FOR BY NAME, and getting this wrong produces a
      // map that builds, type-checks, passes every test and then renders nothing.
      //
      // MapLibre parses tiles and geometry off the main thread. Left to itself it
      // derives the worker's URL AT RUNTIME from its own module URL — `new
      // URL('./maplibre-gl-worker.mjs', import.meta.url)` — which is a string a
      // bundler cannot see, so nothing emits the file and the browser resolves
      // `/assets/maplibre-gl-worker.mjs` to a 404. `new Worker()` does not throw on
      // one: the failure arrives later as an async error event, so the map simply
      // sits there having loaded no tiles.
      //
      // `?worker&url` is what makes Vite treat it as a worker ENTRY — bundling the
      // `./maplibre-gl-shared.mjs` it imports into it, which is why the plain `?url`
      // form (a verbatim copy, its sibling import dangling) is not enough — and hand
      // back the emitted, hashed URL. It rides the same dynamic boundary as the
      // library and the stylesheet, so a viewer who opens no map still downloads none
      // of it.
      import('maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url'),
      import('maplibre-gl/dist/maplibre-gl.css'),
    ]).then(([mod, worker]) => {
      mod.setWorkerUrl(worker.default);
      return mod;
    });
  }
  return mapLibrePromise;
}

// The zoom a single-device map opens at — close enough to read a site, wide enough that
// a lone marker is not floating in featureless tiles. A multi-device map ignores it and
// fits the fleet's bounds instead.
const SINGLE_DEVICE_ZOOM = 14;

export function MapWidget({ widget, data }: WidgetProps<LocationStreamState>) {
  const title = optString(widget.options, 'title');
  const positions = placeable(data.locations);

  // Precedence is per-widget override → tenant → no basemap (ADR-079 §9). The
  // per-widget options are kept, not replaced: they are how someone tries a provider
  // on one board before committing it tenant-wide.
  //
  // 🔴 resolveBasemap is NOT `override ?? tenant` per field. A widget that names its
  // own tile URL does not inherit the tenant's credit line, because that would credit
  // the tenant's provider for tiles it did not serve. The SDK owns that rule so the
  // fence editor and the /dash app cannot disagree with this widget about it.
  const basemap = renderableBasemap(
    resolveBasemap(
      {
        tileUrl: optString(widget.options, 'tileUrl'),
        attribution: optString(widget.options, 'attribution'),
      },
      useTenantBasemap(),
    ),
  );

  // The refusal is checked FIRST and unconditionally: it is a fact about the caller, so
  // it outranks anything about the data, and it must not be reachable only through some
  // "no positions" branch that a later edit could reorder past.
  if (data.forbidden) {
    return (
      <WidgetFrame title={title}>
        <MapNotice
          glyph="🔒"
          heading="Location not permitted"
          detail="You do not have permission to view device locations."
        />
      </WidgetFrame>
    );
  }

  if (data.loading && positions.length === 0) {
    return (
      <WidgetFrame title={title}>
        <MapNotice heading="Loading…" />
      </WidgetFrame>
    );
  }

  if (positions.length === 0) {
    // Two different empty states, because they call for two different actions: bind the
    // widget, or wait for a device to report.
    return (
      <WidgetFrame title={title}>
        <MapNotice
          heading={data.deviceTokens.length === 0 ? 'No devices selected' : 'No positions reported'}
          detail={
            data.deviceTokens.length === 0
              ? 'Bind this widget to a device or anchor and select its location series.'
              : 'None of the selected devices has reported a position yet.'
          }
        />
      </WidgetFrame>
    );
  }

  return (
    <WidgetFrame title={title} bodyStyle={{ padding: 0 }}>
      <LiveMap basemap={basemap} positions={positions} />
    </WidgetFrame>
  );
}

// ---- The tile-less panel ----------------------------------------------------

// PlainPanel draws the positions on a themed background with no basemap at all.
//
// 🔴 IT HAS EXACTLY ONE JOB LEFT, AND IT IS NOT THE UNCONFIGURED CASE. Since the
// bundled world basemap landed, an unconfigured widget renders a REAL MapLibre map
// against public-domain geometry — so this panel is reached only when the map could
// not be BUILT at all. Three ways in, not one: the MapLibre chunk fails to load, the
// bundled-geometry chunk fails to load, or the Map constructor throws (a browser or a
// context with no usable WebGL). They share a catch because they share an answer.
//
// It survives because on that path it is strictly better than an error box. The
// question a map widget answers is "where is my fleet, relative to each other",
// and this still answers it when the renderer cannot load. That is also why
// projectToPanel exists at all, and why deleting either would look like tidying up
// dead code right until someone's dashboard went blank behind a proxy.
function PlainPanel({ positions, note }: { positions: PlaceableLocation[]; note: string }) {
  const points = projectToPanel(positions);
  return (
    <div
      data-testid="map-plain-panel"
      style={{
        position: 'relative',
        width: '100%',
        height: '100%',
        overflow: 'hidden',
        background: css('muted'),
      }}
    >
      {positions.map((position, i) => (
        <Marker
          key={position.deviceToken}
          position={position}
          style={{
            position: 'absolute',
            left: `${points[i].x * 100}%`,
            top: `${points[i].y * 100}%`,
            transform: 'translate(-50%, -50%)',
          }}
        />
      ))}
      <div
        data-testid="map-no-tiles-note"
        style={{
          position: 'absolute',
          left: 0,
          right: 0,
          bottom: 0,
          padding: '4px 8px',
          fontSize: 10,
          lineHeight: 1.3,
          textAlign: 'center',
          color: css('muted-foreground'),
          background: css('card'),
          opacity: 0.9,
        }}
      >
        {note}
      </div>
    </div>
  );
}

// ---- The map ----------------------------------------------------------------

type MapStatus = 'loading' | 'ready' | 'unavailable';

// LiveMap renders MapLibre for BOTH basemap states.
//
// The branch is over the STYLE, not over whether there is a map: a configured tile
// source gets a raster style, and no tile source gets the bundled public-domain
// world. Everything downstream of that — the projection, the markers, the fit, the
// teardown — is the same code on both paths, which is the point. When the two were
// different components, "unconfigured" was a second rendering path that could drift
// away from the real one without any test noticing.
function LiveMap({
  basemap,
  positions,
}: {
  basemap: { tileUrl: string; attribution: string | null } | null;
  positions: PlaceableLocation[];
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const markersRef = useRef<MapLibreMarker[]>([]);
  const [status, setStatus] = useState<MapStatus>('loading');

  const tileUrl = basemap?.tileUrl ?? null;
  const attribution = basemap?.attribution ?? null;

  // Init + teardown for this basemap. Re-runs when the source changes, which is the
  // only thing that invalidates the style.
  useEffect(() => {
    let cancelled = false;
    setStatus('loading');

    // 🔴 The bundled style is FETCHED, not built inline: its geometry sits behind a
    // dynamic import so a configured viewer never downloads a world map they will
    // not look at. Resolved alongside the renderer so the Map is still constructed
    // once, with its final style — nothing re-styles after mount.
    Promise.all([
      loadMapLibre(),
      tileUrl ? rasterStyleFor(tileUrl, attribution ?? undefined) : loadLandStyle(),
    ])
      .then(([maplibre, style]) => {
        const el = containerRef.current;
        if (cancelled || !el) return;
        const map = new maplibre.Map({
          container: el,
          style: style as never,
          center: [0, 0],
          zoom: 1,
        });
        mapRef.current = map;
        setStatus('ready');
      })
      .catch(() => {
        // The renderer or the bundled geometry could not be fetched (offline viewer,
        // a CSP that blocks the chunk). Fall back to the plain panel rather than to
        // nothing: the positions are what the operator came for, and the basemap is
        // the part that failed.
        if (!cancelled) setStatus('unavailable');
      });

    return () => {
      cancelled = true;
      for (const marker of markersRef.current) marker.remove();
      markersRef.current = [];
      mapRef.current?.remove();
      mapRef.current = null;
    };
  }, [tileUrl, attribution]);

  // Sync the markers to the current positions. Rebuilt wholesale rather than diffed:
  // a poll delivers a whole snapshot, the marker count is a dashboard's worth of
  // devices, and a diff here would be state to get wrong for no measurable saving.
  useEffect(() => {
    const map = mapRef.current;
    if (status !== 'ready' || !map) return;

    let live = true;
    loadMapLibre().then((maplibre) => {
      if (!live || mapRef.current !== map) return;
      for (const marker of markersRef.current) marker.remove();
      markersRef.current = positions.map((position) => {
        const el = document.createElement('div');
        el.setAttribute('data-testid', 'map-marker');
        el.setAttribute('data-device', position.deviceToken);
        el.setAttribute('title', describePosition(position));
        el.setAttribute('aria-label', describePosition(position));
        Object.assign(el.style, MARKER_DOT_STYLE);
        // 🔴 setLngLat takes [LONGITUDE, LATITUDE] — x before y, the opposite order
        // from how a coordinate is spoken and written everywhere else on the platform.
        // Swapping them puts a device reporting Atlanta into the Indian Ocean, which
        // renders perfectly.
        return new maplibre.Marker({ element: el })
          .setLngLat([position.longitude, position.latitude])
          .addTo(map);
      });

      if (positions.length === 1) {
        map.jumpTo({ center: [positions[0].longitude, positions[0].latitude], zoom: SINGLE_DEVICE_ZOOM });
      } else if (positions.length > 1) {
        const bounds = new maplibre.LngLatBounds();
        for (const p of positions) bounds.extend([p.longitude, p.latitude]);
        map.fitBounds(bounds, { padding: 40, animate: false });
      }
    });

    return () => {
      live = false;
    };
    // Positions are compared by VALUE: a poll hands back a fresh array every tick, and
    // rebuilding every marker 4 times a minute for an unchanged fleet would make the
    // markers flicker.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, positionsKey(positions)]);

  if (status === 'unavailable') {
    return (
      <PlainPanel
        positions={positions}
        note="Map renderer unavailable — positions are shown without a basemap."
      />
    );
  }

  return <div ref={containerRef} data-testid="map-canvas" style={{ width: '100%', height: '100%' }} />;
}

// positionsKey is the value identity of a marker set: what is drawn, not which array
// object carries it.
function positionsKey(positions: PlaceableLocation[]): string {
  return positions.map((p) => `${p.deviceToken}:${p.latitude}:${p.longitude}:${p.heading ?? ''}`).join('|');
}

// ---- Shared bits ------------------------------------------------------------

const MARKER_DOT_STYLE = {
  width: '12px',
  height: '12px',
  borderRadius: '50%',
  background: css('primary'),
  border: `2px solid ${css('card')}`,
  boxShadow: '0 0 0 1px rgba(0,0,0,0.25)',
  cursor: 'default',
} as const;

// Marker is the tile-less panel's dot. The tiled map builds an equivalent element
// imperatively (MapLibre owns the DOM its markers live in), which is why the styling is
// shared as an object rather than as a component.
function Marker({
  position,
  style,
}: {
  position: PlaceableLocation;
  style: CSSProperties;
}) {
  const label = describePosition(position);
  return (
    <div
      data-testid="map-marker"
      data-device={position.deviceToken}
      title={label}
      aria-label={label}
      style={{ ...MARKER_DOT_STYLE, ...style }}
    />
  );
}

// MapNotice is the widget's non-map state: permission, loading, empty. One component so
// the three read as siblings — a viewer should be able to tell them apart by their words,
// not by their layout.
function MapNotice({ glyph, heading, detail }: { glyph?: string; heading: string; detail?: string }) {
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 4,
        width: '100%',
        height: '100%',
        padding: 12,
        boxSizing: 'border-box',
        textAlign: 'center',
        color: css('muted-foreground'),
      }}
    >
      {glyph ? (
        <div style={{ fontSize: 20, lineHeight: 1 }} aria-hidden="true">
          {glyph}
        </div>
      ) : null}
      <div style={{ fontSize: 13, fontWeight: 600 }}>{heading}</div>
      {detail ? <div style={{ fontSize: 11, opacity: 0.8 }}>{detail}</div> : null}
    </div>
  );
}
