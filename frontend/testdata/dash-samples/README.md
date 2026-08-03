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
dcctl sim create wl --manifest widgetlab
dcctl sim start wl
```

The seed does not matter here, and it is worth saying so because the golden board
fixtures next door are generated at a fixed one. A scenario's device and area *tokens*
come from its token patterns and the device index; only the per-device credentials are
seed-derived. Any seed serves these samples.

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
*scoped* to its `zone` slot under the `manual` strategy, which keeps a device binding
only while that device is still a member of the parent's area — so a manifest that
re-points the zone while leaving the sensor in the old one has the sensor dropped by
the binding cascade. The empty-frame symptom again, from a manifest that reads as
perfectly sensible. The Go-side check enforces the membership these samples depend on.

(The sibling `first` strategy behaves differently enough to matter: it derives the slot
from the parent's first member and ignores whatever a manifest says, so binding such a
slot in a sample is a line that looks like configuration and changes nothing. The Go
check refuses those outright rather than checking their membership.)

## What checks these

Two halves, each on the side that owns the facts, because neither side can do the
other's job:

- **Go** (`backend/sims/dc-simulator/sim/dash_sample_test.go`) owns what actually gets
  bootstrapped: every token here names a real device or area of the owning scenario, the
  parent/child membership above holds, and the directory names a board that still exists.
- **TypeScript** (`frontend/apps/dashboard/src/load.test.ts`) owns the paste path: it
  feeds the committed definition and these manifests through the same `loadDashboard`
  the Render button calls, and checks that every slot the sample names is one the board
  declares, at the kind the board declares it, and that the sample rebinds something the
  definition does not already default to.

Neither can see whether a bound device is *emitting*. A sample naming a real, correctly
placed device that reports nothing would pass both gates and still render empty charts.
Nothing here does that today — the widgetlab scenario keeps its deliberately-silent
sensors out of the gallery's zone — but it is the one empty-board cause left uncovered,
so a new sample is worth eyeballing against a running instance once.
