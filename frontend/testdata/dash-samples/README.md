# `/dash` paste samples

The standalone dashboard viewer (`frontend/apps/dashboard`, served at `/dash`) is
paste-only by design: it is the reference *external* embedder, so it has its own login
and takes a dashboard definition as text rather than reading one out of the console's
session. That leaves anyone trying it with nothing to paste. These are the samples.

Each directory is named for a **dashboard token**, and holds binding manifests for the
board whose definition is committed at `../sim-dashboards/<token>.json`.

## Bootstrap the scenario FIRST — this is not optional

A dashboard definition binds **device and area tokens**, and those entities only exist
once the simulator that owns them has been bootstrapped. Paste a sample at an instance
where its scenario was never run and every widget renders as an empty frame: a board
that looks configured and shows nothing. That is not a bug in the viewer.

For everything here that means the `widgetlab` scenario:

```bash
dcctl sim create wl --manifest widgetlab   # --seed defaults to 1, which is what these bind
dcctl sim start wl
```

The samples can only cover a scenario whose device set is a fixed fixture
(`SimManifest.FixedTopology`). A resizable scenario's tokens depend on the device count
the operator asked for, so a sample naming `bp-therm-004` would be right at one
`--devices` and dangling at another. A test refuses a sample directory for a board
whose scenario is resizable, rather than letting one rot into that state.

## `wl-gallery` — one definition, two manifests

This is the embed contract in the smallest form that actually shows it: the *same*
pasted definition renders against a different zone and a different sensor depending on
the manifest supplied at mount.

1. Sign in, then paste `../sim-dashboards/wl-gallery.json` into **Definition**.
2. Leave **Binding manifest** empty and press Render. The board's own default bindings
   apply: Zone 1, `wl-sensor-01`.
3. Press **Change**, paste the same definition again, and this time paste
   [`wl-gallery/zone-02.json`](wl-gallery/zone-02.json) into **Binding manifest**. The
   identical board now renders Zone 2 and `wl-sensor-02`.

`wl-sensor-02` is assigned to `wl-zone-02` on purpose. The gallery's `sensor` slot is
*scoped* to its `zone` slot, so a manifest that re-points the zone while leaving the
sensor in the old one gets the sensor dropped by the binding cascade — the empty-frame
symptom again, from a manifest that reads as perfectly sensible. The Go-side check
enforces the membership these samples depend on.

## What checks these

Two halves, each on the side that owns the facts, because neither side can do the
other's job:

- **Go** (`backend/sims/dc-simulator/sim/dash_sample_test.go`) owns what actually gets
  bootstrapped: every token here names a real device or area of the owning scenario, the
  parent/child membership above holds, and the directory names a board that still exists.
- **TypeScript** (`frontend/apps/dashboard/src/load.test.ts`) owns the paste path: it
  feeds the committed definition and these manifests through the same `loadDashboard`
  the Render button calls, and checks the result binds what the sample says it binds.
