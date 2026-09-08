# The out-of-tree consumer proof

Run it with [`hack/consumer-proof.sh`](../consumer-proof.sh). That script's header is the
operating manual; this file is the reasoning.

## What it proves

That `@devicechain/client`, `@devicechain/dashboards` and `@devicechain/widgets` work for
somebody who is not us: installed from a real `npm pack` tarball, into an application
outside this repository, built by a bundler this repository does not otherwise use, and
then **rendered in a real browser** — where the assertion is what the map drew, not that
it mounted.

## What it does not prove

ESM, React 19, three arms, one Chrome, one TypeScript. It says nothing about Next.js or
RSC, CJS consumers, or `node16`/`nodenext` module resolution. Those are documented limits
of what we ship, not things this rig quietly covers.

It is also **not a merge gate**. It needs a browser and several minutes of npm installs.
Run it before a release, and after any change to the map runtime contract, the package
`exports`, or the build scripts.

## Why it needs a browser

The defect class this whole arc exists to remove is a worker URL that resolves to
something useless. Everything cheaper than a browser has been measured against a
deliberately broken build and found unable to see it:

| instrument | on a build with a dead worker |
| --- | --- |
| the package build | exit 0, defect in the artifact |
| the consumer's build (vite, webpack) | exit 0, for two different real defects |
| `.d.ts` / typecheck | clean — the URL is a `string` either way |
| the browser console | **zero errors**, on one of the two |
| the DOM | two canvases, eight markers, no fallback panel, no notice |
| the served requests | worker URL answered **200** |

So the rig gates on pixels. The board carries two map widgets, and the second one is the
instrument: it names no tile source, so it falls back to the bundled world basemap, which
is **GeoJSON — parsed in the worker**. Its land polygons are therefore a direct read of
worker output, while the ocean under them is a flat `background` paint that renders
without one. Land colour present ⇒ the worker loaded, ran and returned geometry. Land
colour absent, ocean everywhere ⇒ it did not.

That widget was added after the first version of this rig passed completely while
**never fetching the worker at all** — a raster-only map does not use one, so every
worker assertion was vacuous. A rig whose central claim cannot fail is worse than no rig.

## The three arms

The application source (`app/`) is byte-identical in all three. The only file that
differs is `runtime.ts`, which is the contract each host has to satisfy — so a difference
in outcome is attributable to the bundler and to nothing else.

| arm | what it exercises |
| --- | --- |
| `vite/` | the ready-made runtime the package ships (`@devicechain/widgets/vite`) |
| `webpack/` | the bundler-native recipe: MapLibre's worker as a second webpack entry |
| `copy/` | the bundler-agnostic recipe the published README gives: the worker files copied into the served output |

A Vite-only proof would certify the one bundler that already worked — which is the point
of the whole slice. The `copy` arm exists because that recipe is **published to
npmjs.com**, and a published recipe nothing exercises is exactly how the broken one got
there.

## What it found on its first run

The webpack recipe documented in `map-runtime-context.tsx` and in the widgets README —
`new URL('maplibre-gl/dist/maplibre-gl-worker.mjs', import.meta.url)`, or copying that
one file into static output — **is broken**. webpack emits the target as an asset, copied
verbatim; the copy still begins `import … from './maplibre-gl-shared.mjs'`; that sibling
is never emitted. The worker dies on its first line, and the failure is completely quiet.
Both documents now carry the two recipes this rig verifies, and both are arms here.

## The negative controls

`hack/consumer-proof.sh control` plants three defects. Each must fail **the assertion it
was aimed at** — a control that merely goes red proves nothing, since it goes red for a
stale install or a rig somebody just broke — and each is then restored and required to
pass again through the same pipeline.

1. **A deleted export.** `MapRuntimeProvider` is removed from the widgets entry, rebuilt,
   repacked, reinstalled. The consumer's build must fail *and name it*.
2. **A reintroduced bundler dialect.** `?worker&url` is planted back into `map.tsx` the
   way the pre-contract code had it. webpack does **not** reject it — it strips the query,
   bundles the module and exits 0 — so the control is required to fail on the land pixels.
3. **An unwired host.** The provider is removed from the application. Both widgets must
   render the "Map runtime not configured" notice, asserted positively: "no map appeared"
   is equally true of a page that never loaded.

## Maintenance

- The colours in `harness/drive.mjs` (`LAND`, `OCEAN`) mirror `map-geometry.ts`. If those
  change, this fails closed — no region matches, and the assertions report it — rather
  than passing on the wrong canvas.
- The arms pin dependency **ranges**, not exact versions, on purpose. A consumer proof
  that pins its toolchain forever stops proving anything about the ecosystem it is
  supposed to be testing.
- The work directory lives **outside the repository** (`$TMPDIR/dc-consumer-proof`).
  Node resolves modules by walking parents, so an arm inside the repo would also see
  `frontend/node_modules` and a missing dependency would resolve anyway.
