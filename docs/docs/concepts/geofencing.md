---
sidebar_position: 16
title: Geofencing
---

# Geofencing

A **geofence** is a named boundary on the Earth's surface. Devices report where they are; detection rules ask whether a reported position is inside a fence. That is the whole idea — but the two halves of it, *where the device was* and *what the fence was*, both move over time, and getting an honest answer means being careful about which version of each you compare.

:::note Status
Available: geofence authoring in the console (draw, edit, delete) or over GraphQL, `POLYGON_2D` boundaries with holes, spherical containment in detection rules, and a frozen archive of every fence set with a browsable history. Planned: additional geometry kinds — the schema reserves `POLYGON_2_5D` and `VOXEL_3D`, both rejected on write today.
:::

## Drawing a fence {#drawing-a-fence}

In the console, geofences live under **Areas → Geofences**. Click the map to place a corner, click the first corner to close the shape, and save. On an existing fence you can drag a corner to move it, Alt-click a corner to remove it, or click the small handle on an edge to add a corner there.

A fence carries a **token** — the identifier rules use to name it — plus an optional name and description. The token is fixed once created.

### The map behind your fence {#no-default-basemap}

The editor draws on the tenant's **basemap**, which a new instance ships already configured — so tiles appear without anyone setting anything up.

A tenant admin changes the tile source once, for everyone, under **Basemap**; the fields in this editor are a personal override on top of that, remembered in your browser only, for trying a provider before committing it tenant-wide. See [Basemaps](./basemaps.md) for how the tiers resolve, why the tile URL and its attribution move together, which provider ships by default, and how to protect a provider API key.

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

For how detection rules are authored and evaluated, see [event processing](./event-processing.md). For the location data itself, see [connecting a device](../guides/connecting-a-device.md).

## Permissions {#permissions}

Reading geofences and their history requires `device:read`; creating, changing and deleting them requires `device:write` — the same authorities that govern the rest of the device registry.
