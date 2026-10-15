---
sidebar_position: 11
title: Widget Versions and Compatibility
---

# Widget Versions and Compatibility

A saved dashboard records which widget implementation authored each widget. When you upgrade a
widget package, DeviceChain decides for each stored widget whether the new package can still render
it. This page explains that decision and what to do about each outcome.

## What is stored

A widget from a registered package carries a `widget` reference next to its `type`:

```json
{
  "id": "tank-1",
  "type": "acme/tank-level",
  "widget": { "version": "1.2.0", "optionsVersion": 1, "sdkApiVersion": 1 },
  "layout": { "base": { "mode": "grid", "col": 0, "row": 0, "cols": 4, "rows": 3 } },
  "datasource": { "kind": "slot", "slot": "tank", "measurements": ["level"] },
  "options": { "unit": "%" }
}
```

The console records the reference when an author adds a widget, copying `version`,
`optionsVersion` and `sdkApiVersion` from the registration it was added from. This applies to
built-in widgets too: each built-in carries its own widget version, starting at `1.0.0`, which is
not the platform version and changes only when its rendering changes in a way worth pointing out.
Built-in widgets saved before references existed have none and keep rendering.

## The compatibility rule

A stored widget is compatible with an installed package when its `id`, its `optionsVersion` and its
`sdkApiVersion` match. The `version` is provenance and does not decide compatibility.

A reference saved before the console recorded `sdkApiVersion` has only `version` and
`optionsVersion`. It is matched on `id` and `optionsVersion` and keeps rendering; the next time an
author replaces the widget, the new reference records all three.

| Stored widget against the installed package | Result |
| --- | --- |
| Same `id`, `optionsVersion` and `sdkApiVersion`, same `version` | Renders. This is the only case in which the widget is rendered by the exact release that authored it. |
| Same `id`, `optionsVersion` and `sdkApiVersion`, different `version` | Renders. The console's settings panel for the widget shows a non-blocking notice naming both versions, so a changed look is not a surprise. |
| Different `optionsVersion` | Placeholder, with the reason. The stored options mean something different to the installed package. |
| Different `sdkApiVersion` | Placeholder, with the reason. |
| Package not installed in this application | Placeholder: the package is not installed in this host. |
| Custom `id` with no `widget` reference | Placeholder: the installed widget cannot be matched to it. |
| A `widget` reference that is malformed, on any widget | Placeholder: the stored version information is not valid. |

A placeholder takes its place on the canvas and keeps the widget's original JSON, including the
version reference and any fields it does not recognize. It opens no data subscription and offers no
actions. Loading the dashboard, moving the placeholder and saving a draft all preserve its data, so
installing the missing package later brings the widget back with no loss. The console refuses to
publish a new dashboard version while any widget is a placeholder; see
[publishing while a widget is unresolved](./adding-widget-packages.md#publishing-while-a-widget-is-unresolved).

If you build your own host, `@devicechain/dashboards` exports the same decisions:
`unresolvedWidgets(definition)` lists the placeholders on a parsed definition with their reasons,
and `widgetVersionNotice(id, reference, descriptor)` returns the provenance notice, or `undefined`
when the versions match.

## Upgrading a package

Choose the version bump by what changed:

- **A bug fix or visual change that does not alter what stored options mean:** bump `version`
  (`1.2.0` to `1.2.1`). Existing dashboards keep rendering, and an author who opens one of these
  widgets in the console sees the version notice.
- **A change to the meaning or shape of stored options:** bump `optionsVersion`, and bump `version`
  as well. Existing widgets become placeholders until an author replaces them, because there is no
  automatic options migration in this release. Prefer adding an optional option with a default
  over changing an existing one, so you do not need this bump.
- **A move to a new SDK contract generation:** `sdkApiVersion` changes, and a host accepts only
  registrations for a generation it supports. Widgets whose stored reference records the old
  generation become placeholders, with the reason, until an author replaces them.

Upgrading a package never rewrites a dashboard or an already-published version. To move a widget
onto a new `optionsVersion`, an author replaces it, edits its settings and publishes a new
dashboard version. A published version authored with `1.2.0` that is now rendered by `1.2.1` is not
an exact replay; exact replay needs the same `version`. A host installs one version of each id.

## Missing packages in a published dashboard

Because the implementation is not part of the dashboard, a viewer application that lacks a package
shows the placeholder, not an error page, and the rest of the dashboard renders. Keep the console
and every viewer on the same set of widget packages.
