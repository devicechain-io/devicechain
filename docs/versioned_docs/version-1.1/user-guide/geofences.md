---
title: Geofences
sidebar_position: 10
---

import GuideShot from '@site/src/components/GuideShot';

# Geofences

A **geofence** is a boundary drawn on a map. Detection rules test the positions your devices
report against it, for example to raise an alarm when a tracker leaves a site. This chapter shows
how to draw and edit fences in the console, and how to see what a fence looked like in the past.
It is for tenant users and administrators who maintain fences. For the rules that use them, see
[using a fence in a rule](/concepts/geofencing#using-a-fence-in-a-rule).

Geofences are under **Organization → Areas → Geofences** in the sidebar.

## The geofence list

The list shows each fence's token, name, number of corners and creation date. Click a row to open
the fence.

<GuideShot id="geofences-list" alt="The Geofences list with one fence, acme-campus, named Acme campus perimeter, with 4 corners. A New geofence button is at the top right." />

## Drawing a new fence

Click **New geofence**. The drawer asks for:

- **Token**: the identifier rules use to name the fence, for example in `geo.inFence("acme-campus")`.
  The button beside it generates one from your instance's token pattern. The token cannot change
  after you create the fence.
- **Name** and **Description**: free text you can change at any time.
- **Boundary**: the shape, drawn on the map.

<GuideShot id="geofences-new" alt="The New geofence drawer: Token, Name and Description fields above a Boundary map of Europe and Africa. Under the map, a counter reads 0 of 511 corners, with Undo last point and Clear all points buttons, and a collapsed Basemap section." />

To draw the boundary:

1. Zoom and pan the map to your site.
2. Click the map to place each corner.
3. Optionally, click the first corner again to close the shape, so that further clicks on the map
   no longer add corners.
4. Click **Create geofence**.

While you draw, the counter under the map shows how many corners you have placed and the most
allowed. **Undo last point** removes the latest corner, and **Clear all points** starts over.

The console checks the shape as you go. A boundary needs at least three corners, and two corners
cannot sit in the same place; the console will not save a shape that breaks either rule. It also
warns you if edges cross, because a shape with crossing edges has no clear inside. That check is an
approximation, so it does not stop you from saving: the server's answer decides. See
[boundary rules](/concepts/geofencing#what-a-boundary-may-be) for the details.

### The map behind the fence

The map shows your tenant's basemap, which a tenant administrator sets once for everyone under
**Settings → Map**. The **Basemap** section under the map lets you try a different tile provider:
enter a **Tile URL** and its **Attribution**. That override is remembered in your browser only. It
is not shared with other users and is not stored on the server. The tile URL and the attribution go
together: a tile URL with no credit line is ignored, and the tenant's basemap is shown instead.

If no tile source is configured anywhere, the map falls back to a built-in world map with
continents and borders only. Drawing still works, and the coordinates you place are exact. See
[Basemaps](/concepts/basemaps).

## Editing a fence

Open a fence to see its detail page. The **Basic** tab shows the token (read-only), the name, the
description and the boundary on the map.

<GuideShot id="geofence-detail" alt="The detail page of the Acme campus perimeter fence, on the Basic tab. The read-only Token, the Name and the Description are above a street map of downtown Chicago, with the fence's outline, its top corners and the handles on its edges drawn on it." />

On the map, you can:

- drag a corner to move it,
- Alt-click a corner to remove it, or
- click the small handle in the middle of an edge to add a corner there.

Click **Save changes** to apply your edits. **Delete** at the top right removes the fence after you
confirm. A deletion cannot be undone. A rule that still names the deleted fence's token can no
longer evaluate it and reports evaluation errors; see
[unknown or missing fences](/concepts/geofencing#unknown-fence).

If a fence uses a shape this editor cannot draw, the console says so. The fence is stored intact
and is still enforced; only editing it here is unavailable. Leave it as it is rather than
recreating it.

## History

Each time the set of fences changes (a fence is created, its boundary is edited, or it is
deleted) the platform freezes every fence's shape into a new **fence set version**. Renaming a fence
or editing its description does not create a version, because the shapes stay the same.

Location events carry the version that was in force when they arrived. So when you replay a rule
over past events, they are judged against the fences as they were then.

The **History** tab shows this fence at any version. Use **Older** and **Newer**, or type a version
number. **Current** appears when you are looking at the latest version. The shape stored at the chosen version is drawn solid,
and the fence's current shape is drawn dashed behind it, so you can compare them.

<GuideShot id="geofence-history" alt="The History tab of the Acme campus perimeter fence. The Fence set version control shows 1, with Older and Newer buttons and the Current label. The square boundary is drawn on the built-in world map, solid at version 1 and dashed for the current shape, with the legend below." />

The History map always uses the built-in world map, without tiles, so only the shape is shown.

If the fence did not exist at the version you pick, the tab says so: it was created later, or a
fence with this token was deleted and the token reused.

## Permissions

Reading fences and their history requires `device:read`, and deleting one requires `device:write`.
Creating or changing a fence also requires `location:read`, because drawing a fence asks a question
about where devices are. See [permissions](/concepts/geofencing#permissions).

## Related

- [Geofencing](/concepts/geofencing): boundary rules, spherical containment, fence-set
  versions, and using a fence in a rule.
- [Basemaps](/concepts/basemaps): where the map tiles come from.
- [Assets, customers and areas](/user-guide/assets-customers-areas)
