---
title: Geofencing
---

# Geofencing

A **geofence** is a named boundary on the Earth's surface. Devices report where they are; detection rules ask whether a reported position is inside a fence. That is the whole idea — but the two halves of it, *where the device was* and *what the fence was*, both move over time, and getting an honest answer means being careful about which version of each you compare.

:::note Status
Available: geofence authoring in the console (draw, edit, delete) or over GraphQL, `POLYGON_2D` boundaries with holes, spherical containment in detection rules, and a frozen archive of every fence set with a browsable history. Planned: additional geometry kinds — the schema reserves `POLYGON_2_5D` and `VOXEL_3D`, both rejected on write today.
:::

## Drawing a fence {#drawing-a-fence}

In the console, geofences live under **Areas → Geofences**. Click the map to place a corner, click the first corner to close the shape, and save. On an existing fence you can drag a corner to move it, Alt-click a corner to remove it, or click the small handle on an edge to add a corner there.

A fence carries a **token** — the identifier rules use to name it — plus an optional name and description. The token is fixed once created; the console renders it read-only when editing, and the API refuses an update that would change it. The name is free to change at any time, and changing it breaks nothing.

The reason is that a rule names a fence by its **token**, inside rule text the platform cannot rewrite on your behalf. A rename would leave every `geo.inFence("old-token")` naming nothing. If you genuinely need a different token, create the new fence, repoint the rules that name the old one, then delete the old fence — which is the order that makes you deal with the rules rather than discover them later.

### The map behind your fence {#no-default-basemap}

The editor draws on the tenant's **basemap**, which a new instance ships already configured — so tiles appear without anyone setting anything up.

A tenant admin changes the tile source once, for everyone, under **Settings → Map**; the fields in this editor are a personal override on top of that, remembered in your browser only, for trying a provider before committing it tenant-wide. See [Basemaps](./basemaps.md) for how the tiers resolve, why the tile URL and its attribution move together, which provider ships by default, and how to protect a provider API key.

If no tier has a tile source — an operator set the instance default to `{}` and the tenant set nothing — the editor falls back to a bundled world map: public-domain continents and country borders, compiled into the console, showing nothing at street zoom. Drawing still works and the coordinates you place are exact either way, because it is the same projection a tiled map uses.

## What a boundary may be {#what-a-boundary-may-be}

Boundaries are stored as GeoJSON, with positions in `[longitude, latitude]` order and rings closed explicitly (the first position repeated as the last). The console handles that for you; an integration authoring over GraphQL supplies the document directly.

Two limits apply per fence set: at most **512 positions** across all of a fence's rings, counting each ring's closing position, and at most **100 fences** per tenant. The first exists because containment costs time proportional to the number of vertices on every location event, and the rule-authoring cost gate cannot see a number that lives on a fence rather than in a rule.

A boundary must also **bound an area**. A ring whose edges cross — a bow-tie — has no well-defined interior, so a containment question about it has no honest answer. Rings like that are refused when you save, by the same check the detection engine applies when it compiles a rule. That matters more than it sounds: before the check ran at authoring time, such a fence saved cleanly, sat in the registry looking healthy, and failed only later when a rule finally named it.

:::tip The console warns before the server refuses
While drawing, the console flags a self-crossing shape immediately. Its check is a flat approximation of the spherical one the server performs, so the two can disagree on very large fences or ones spanning the antimeridian — which is why the console only *warns*. The server's answer is the one that decides.
:::

## The maths is spherical {#the-maths-is-spherical}

Containment is computed on a sphere, not on a flat map, and that is not a refinement. Treating longitude and latitude as `x` and `y` gives wrong answers in two places real fences actually sit:

- **Across the antimeridian.** A fence spanning 179°E to 179°W is 2° wide. Read as flat coordinates it becomes a 358°-wide band covering most of the world — answering "inside" for a device in the Atlantic and "outside" for one standing in the fence.
- **At high latitudes.** The shortest path between two points at the same latitude bows toward the pole, so a "rectangle" drawn as four corners is not bounded by lines of constant latitude. On a box covering 10°W–10°E and 80°–81°N, a point at 80.05°N is *outside* the real fence at the centre and *inside* at the edges. Flat maths says both are inside.

### Where the edge counts {#where-the-edge-counts}

Three answers you may need to predict, all decided one way and applied uniformly:

- **The boundary is inside.** A position sitting exactly on a fence's edge is contained. Left to the underlying geometry library, the boundary would be split between adjacent regions — so two fences sharing an edge would each claim part of it and neither would claim the rest. An explicit on-edge test avoids that, with a tolerance of about 6 mm on the ground.
- **A hole's edge is inside too**, by the same rule. Position strictly inside a hole → outside the fence. Position on the hole's ring → inside the fence. So two adjacent fences, or a fence and its own hole, never disagree about a point they share.
- **Ring direction does not matter.** Clockwise and counter-clockwise spellings of the same ring give the same answer; the winding is normalised rather than trusted, so an integration authoring GeoJSON over the API does not have to get it right. (The one shape this cannot rescue is a "fence" so large it wraps most of the globe, where "the smaller region" is ambiguous — but the 512-position limit and the requirement to bound an area put that well outside anything a real fence looks like.)

## Fences change, and history has to survive it {#fences-change}

Every geofence change — create, edit, delete, even renaming one — **freezes the whole fence set into a new version**. Each version stores the geometry of every fence as it stood at that moment, so the shapes are preserved even after the fences themselves are edited or deleted.

Every location event is stamped with the fence-set version in force when it arrived. This is what makes replaying a rule over past events meaningful: last week's events are judged against last week's fences. Without it, a preview would answer from today's shapes and be quietly fictional — confident, plausible, and about a world that never existed.

The **History** tab on a geofence shows the boundary as it was at any version, with the current shape drawn behind it for comparison. Three answers are possible and they mean different things:

| What you see | What it means |
| --- | --- |
| The boundary | The shape stored under this token at that version. |
| "Not in the set at version *N*" | The fence did not exist then — created later, or deleted and its token reused. Not the same as existing with no shape. |
| "Shape this viewer cannot draw" | It was in the set and was enforced; only the console cannot render it. |

Because deleting a fence is permanent and frees its token for reuse, an entry in an old version tells you the shape stored under a token at that time — not necessarily that it belonged to the fence you are looking at now.

## Using a fence in a rule {#using-a-fence-in-a-rule}

Detection rules reach a fence by token:

```text
geo.inFence("yard-perimeter")
```

The predicate answers for the position on the event being evaluated, against the fence set that event was stamped with. A rule naming a fence that cannot be compiled — because its ring does not bound an area — fails to compile rather than answering arbitrarily.

### A fence test and a measurement test cannot share a condition {#fences-and-measurements}

A condition that calls `geo.inFence(...)` **and** reads a measurement is refused when you publish it:

```text
geo.inFence("yard") && m["temp"] > 80    ← refused
```

Not a style rule — no event could ever satisfy it. A location event reports a position and carries no measurements; a measurement event carries readings and reports no position. A condition needing both is fed nothing, forever, and would sit in your rule list reporting healthy while never firing. Refusing at publish is the only place the two halves are visible together; at evaluation each is just a sample that did not qualify.

Express it as two rules instead — one on the fence, one on the measurement — or, if the value you are testing changes rarely, put it on the device as an **attribute**, which a location event does carry.

### When a fence a rule names is not there {#unknown-fence}

`geo.inFence("typo")` compiles and publishes: nothing checks at publish time that the token names a real fence. At evaluation the call cannot answer, and the platform will not invent one — the sample is **skipped and counted as an evaluation error**, never answered "outside". Answering "outside" would be worse than useless: for a rule holding a condition over time it would look like the device leaving the fence.

Four situations produce this, and only the first is a mistake:

| Situation | What you see |
| --- | --- |
| The token is misspelled, or the fence was deleted | Evaluation errors on every location event, from the moment the rule goes live |
| Previewing a rule over events from **before the fence was drawn** | Errors for the whole stretch before it existed — the fence genuinely was not in the set then |
| A rule authored against a fence that exists in a *later* version than the events being replayed | The same, and for the same reason |
| A tenant's very first fence rule, in the seconds after it is published | Transient; the engine loads the fence set as the rule arrives |

Evaluation errors are surfaced per rule on the authoring preview and on the detection engine's rule health, so this is visible — but nothing points at the geofence as the cause. If a fence rule is producing errors and nothing else, check the token first.

### What the engine keeps in memory {#fence-set-retention}

The live engine holds the **four most recent fence-set versions** per tenant — the current one plus three superseded. That is sized for events still in flight, which are seconds to minutes old, so reaching the bound takes **four fence edits while an event is between the ingest path and the engine**. An event stamped with a version that has been evicted reports the same counted evaluation error as an unknown fence.

Nothing is lost from history when that happens: every version's snapshot is stored durably, and the preview and replay paths read from there rather than from the live cache. It is only live evaluation that is bounded, and it recovers on its own — the next events carry the current version.

The engine also re-reads each tenant's current fence set from stored history every few minutes. Normally that changes nothing, because a fence edit is announced to the engine as it happens. It matters in one uncommon case: saving a fence never fails because the announcement could not be sent, so if that announcement is lost the engine would otherwise keep evaluating against the older version until it next restarted. The periodic re-read closes that by itself, which means **a fence edit takes effect within a few minutes at the outside, even if its announcement never arrives** — no restart, and no need to re-save the fence.

For how detection rules are authored and evaluated, see [event processing](./event-processing.md). For the location data itself, see [connecting a device](../guides/connecting-a-device.md).

## Permissions {#permissions}

Reading geofences and their history requires `device:read`, and deleting one requires `device:write` — the same authorities that govern the rest of the device registry.

**Creating or changing a fence additionally requires `location:read`.** Drawing a fence is not only a write: it asks a question about where devices are. Someone who could create fences with `device:write` alone could place a small one, watch whether any rule reacts, move it, and read a fleet's positions out of the answers — without ever holding the authority to see a coordinate. Deleting stays on `device:write`, because removing a fence asks nothing.
