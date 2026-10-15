---
title: Dashboards
sidebar_position: 8
---

import GuideShot from '@site/src/components/GuideShot';

# Dashboards

A dashboard arranges live widgets (charts, gauges, cards, alarm tables, command buttons, maps) on
one page. Dashboards belong to the tenant, and each one is versioned: you edit a draft, then publish
it as the version everyone else sees.

This chapter is for people who build and maintain dashboards. It covers the dashboard list,
creating a dashboard from scratch, from a template or from an exported file, viewing it, editing it
on the canvas, configuring widgets, previewing with synthetic data, exporting, and publishing and
rolling back versions. The screenshots use a test tenant whose *Widget Lab* dashboard holds one of
every built-in widget. For how dashboards work underneath, see [Dashboards](/concepts/dashboards).

:::note Who can do what
Every member of the tenant can open a dashboard. Creating, editing, publishing and deleting one
needs the dashboard write permission. A member without it is shown the published version, never the
draft. If a dashboard has never been published, they are told to ask an author to publish it.
:::

## The dashboard list

Choose **Dashboards** in the sidebar.

<GuideShot id="dashboards-list" alt="The Dashboards page with Import dashboard and New dashboard buttons. Two dashboards are listed, Widget Lab — Stress and Widget Lab, each with a description, an updated time, and Clone and Delete actions." />

Each row shows the dashboard's name, description and when it was last updated. Select a row to open
it. **Clone** makes a copy under a new token, and **Delete** removes the dashboard after you
confirm. Deleting cannot be undone.

## Creating a dashboard

**New dashboard** opens a drawer with two ways to start.

<GuideShot id="dashboards-new" alt="The New dashboard drawer with Token, Name and Description fields and a Create dashboard button. Below, under Or start from a template, three templates are offered: Fleet overview, Device detail and Alarms board, each with a one-line description." />

- **From scratch.** Type a token, the dashboard's permanent id, or use the button beside the field
  to generate one. The token is the only required field; a name and a description are optional. The
  field marks a token that is already taken. Choose **Create dashboard** and the empty dashboard
  opens.
- **From a template.** Under **Or start from a template**, pick **Fleet overview** (alarm counts by
  severity, an alarm table and a featured device), **Device detail** (latest values, a gauge, a
  telemetry chart, location and alarms for one device) or **Alarms board** (alarm counts and tables
  for the whole tenant). The template opens in the import review described below, where you bind it
  to your own devices before it is created.

Template measurement names such as `temperature` and `humidity` are examples. Change them in the
editor to the ones your devices report.

## Importing a dashboard

**Import dashboard** creates a new dashboard from JSON exported from another dashboard or another
tenant.

<GuideShot id="dashboards-import" alt="The Import dashboard drawer. It explains that the new dashboard is saved as a draft under a new token, and that nothing is overwritten or published. It has a Dashboard JSON text box, a hint that the dashboard can be up to 1 MiB not counting indentation, and Choose file and Review buttons." />

1. Paste the JSON into **Dashboard JSON**, or use **Choose file** to load a `.json` file.
2. Choose **Review**. The console checks the definition and lists anything you should look at
   before it is saved:
   - **Bindings to review**: slots that are not bound yet, or that name a device, customer, area or
     asset that does not exist in this tenant. Pick one from this tenant for each, or leave the picker
     at *Leave unbound* and bind it later.
   - widgets this console cannot show, which are kept as placeholders and block publishing until
     they are installed or removed;
   - widget options to fix;
   - external sites the dashboard loads images or map tiles from.
3. Give the new dashboard a token and choose **Create draft**.

An import never overwrites an existing dashboard, and it is never published for you. **Clone** and
the templates use the same review.

## Viewing a dashboard

Opening a dashboard shows it with live data.

<GuideShot id="dashboard-view" alt="The Widget Lab dashboard in view mode, with Preview, Export, History, Edit and Delete buttons. Widgets include a title label, a latest-value card and a gauge for temperature on wl-sensor-01, an Active alarms count for wl-zone-01, an image, a Temperature and humidity chart, and a Latest values table listing battery, humidity, pressure and temperature." />

The buttons along the top:

- **Preview** swaps live data for generated data (see [Previewing with synthetic data](#previewing-with-synthetic-data)).
- **Export** copies or downloads the dashboard (see [Exporting](#exporting)).
- **History** opens the version history (see [Versions](#versions)).
- **Edit** switches to the editor.
- **Delete** removes the dashboard.

Widgets that carry actions, such as acknowledging an alarm or sending a command, work here too. The
platform checks each action against your own permissions.

## Editing on the canvas

**Edit** turns the dashboard into a canvas. The buttons change to **Add widget**, **Preview**,
**Export**, **History**, **Save** and **Done**, and a bar of layout settings appears.

<GuideShot id="dashboard-edit" alt="The Widget Lab dashboard in edit mode. The header shows Add widget, Preview, Export, History, a disabled Save and Done. A settings bar shows Title Widget Lab, Columns 12, Min cell 56, Gutter 8, Padding 0 and Size Fill area. Widgets are outlined on the canvas; some show a dash or No data yet." />

- **Title** is the dashboard's name.
- **Columns**, **Min cell**, **Gutter** and **Padding** set the grid widgets snap to. The cells
  are square and stretch to fill the width; **Min cell** is the smallest a cell may get, in
  pixels. **Gutter** is the space between cells and **Padding** the margin around the dashboard.
- **Size** decides how the dashboard fills the page: **Fill area**, **Fixed width** or **Fixed
  height**.
- **Time range** is the span the dashboard's charts draw when it opens: 15m, 1h, 6h, 24h or 7d.
  One hour is the default. Viewers can pick another from the header and pause the board; see
  [Viewing dashboards in /dash](/user-guide/viewing-dashboards-in-dash#time-range-and-pause).

Drag a widget to move it. A dashed outline shows the cells it will take when you let go, and it
lands exactly there. A widget in its way moves to the nearest free space above or below, so
dropping one widget onto another swaps them. Nothing else moves: a gap you left on purpose stays
open. Widgets on the grid never overlap, and a new widget goes in the first free space it fits. Select a widget
and drag a handle on its edge or corner to resize it. The size shows as columns by rows, such as
*2×3*, while you drag, and the widget stops at the smallest and largest size it allows. A widget
that can only be resized in one direction has handles on that side only. Press **Esc** while
dragging to cancel. With a widget focused, the arrow keys move it one cell and **Shift** with the
arrow keys resizes it. A widget outside the sizes it supports, or running past the right edge,
carries a note saying so. A widget that has not received data yet shows a dash or *No data yet*.

The dashboard measures the space it is given, not the screen. While the columns fit at **Min
cell** or larger it shows your layout as you arranged it, with the cells stretching to fill the
width, so a wall display gets bigger cells rather than more of them. In a narrower space it uses as
many columns as fit and pours the widgets into them in reading order, top to bottom and left to
right. Below 600 px wide, as on a phone, each widget takes its own row at the full width, so the
dashboard reads as a single column. Each widget keeps its size within the limits it allows, and a
widget placed relative to another moves with it.

A dashboard saved by an earlier release is converted to the current grid when it opens. The
conversion is approximate, so the editor shows a note asking you to review the layout. Saving
keeps the dashboard in the current format and removes the note.

A timeseries chart saved by an earlier release kept a number of samples instead of a span of time.
A sample count has no span without knowing how often the device reports, so the chart is not
converted to a guessed span. It follows the dashboard's time range instead, and the editor names
the charts concerned. Set a **Time span** on any chart that should differ, then save.

**Save** stores your changes in the draft. It is greyed out until there is something to save.
**Done** returns to the view; unsaved changes stay, and **Save** remains available there. If you
close the tab or use the **Dashboards** back link with unsaved changes, the console asks before
discarding them. Choosing another page in the sidebar does not ask, so save first. Saving changes the
draft only. Viewers see nothing new until you publish a version.

### Adding a widget

**Add widget** opens the widget menu, grouped by what each widget does.

<GuideShot id="dashboard-add-widget" alt="The Add widget menu open in edit mode, grouped into Telemetry (Timeseries chart, Latest card, Gauge, Table), Static (Label, Image), Alarms (Alarm table, Alarm count), Control (Command button), Navigation (Entity selector) and Location (Map). Each entry shows its id and version, for example timeseries-chart · v1.0.0." />

| Group | Widgets |
|---|---|
| Telemetry | Timeseries chart, Latest card, Gauge, Table, Linear gauge, Level gauge, Battery, Status light, Signal bars, Compass, Progress ring, Stat card, Bar chart, Pie chart, Fleet table |
| Static | Label, Image |
| Alarms | Alarm table, Alarm count |
| Control | Command button, Command slider, Command toggle |
| Navigation | Entity selector, which lets a viewer choose which device or entity the dashboard shows |
| Location | Map, which draws on the tenant's map (see [Tenant settings](/user-guide/tenant-settings)) |

Each entry shows the size, in cells, the widget is added at, such as *4×3*. Choose one to add it to
the first free space on the canvas, then configure it.

### Configuring a widget

Select a widget on the canvas to open its settings on the right. **Grid**, **Relative to…** and
**Delete** appear on the widget itself.

<GuideShot id="dashboard-widget-config" alt="Edit mode with the Temperature latest card selected, showing Front and Delete buttons on the widget. The settings panel on the right, titled Latest card, shows Title Temperature, a Context section with Binds to set to Scoped to a parent, Parent slot zone and Strategy Manual pick, a Data source section explaining it is derived from the parent context, slot sensor, and a Measurement field." />

The settings depend on the widget. Common ones:

- **Title** shown on the widget.
- **Context**: what the widget is bound to. **Root context** binds it directly to a device or
  entity. **Scoped to a parent** makes it follow another widget's context, for example a device
  inside the building chosen elsewhere on the dashboard. With a parent, **Strategy** decides whether
  it follows the parent's **First member** or a member you choose (**Manual pick**).
- **Data source** picks the device or entity the widget reads. For a widget scoped to a parent, it
  explains that the widget follows the parent.
- **Measurement** and display options such as units, range and precision.
- **Time span** (timeseries chart): how much history the chart draws, in seconds, from 60 to
  604800. Leave it empty and the chart follows the board's time range, which viewers can change.
  Set it and the chart keeps that span whatever range a viewer picks.
- **Statistic** (stat card): which figure of the span to show: average (the default), minimum,
  maximum, sum, number of readings, or the latest reading. The card prints the statistic and the
  span it covers under the figure, for example "Maximum · 1h". The same **Time span** setting
  pins the card to its own span. A card says why it has no figure (the data source cannot compute
  aggregates, the read failed, or there were no readings in the span) instead of showing a number
  it could not compute. The number of readings of a span with none is 0.
- **Order** (bar chart, pie chart): the measurements in the order the data source lists them, or
  sorted smallest or largest first. A measurement that has not reported yet comes last. A bar chart
  draws columns in a wide widget and rows in a tall one.
- **One bar per** (bar chart): *Measurement* (the default) draws a bar for each measurement of one
  device. *Device* draws a bar for each device of the group the widget is bound to, for the one
  **Measurement** you choose, so the data source must be a device group, not a single device. A
  group too large to draw is cut to its first bars in the chosen order, and the chart says "Showing
  N of M devices".
- **Fleet table**: one row per device of the group the widget is bound to and one column per
  measurement the data source selects. Set the group on the widget's data source and pick the
  measurements to show. **Columns** gives a heading, unit and precision per measurement;
  **Thresholds** colour the cells of one measurement's column (the normal band keeps the table's
  colour); **Stale after** marks a reading older than that many seconds as stale, and without it
  the table only shows each reading's age. Click a column heading, or press it from the keyboard,
  to sort; **Order by** and **Largest first** set the starting order, and devices with no value
  for the sorted column always come last. **Devices per page** pages a long fleet. **Target slot**
  makes the device name a link that selects that device in the widgets bound to the slot. On a
  narrow board the table becomes a card per device. A cell with no number says why: *no data in
  range*, *does not report this* (the device reports other measurements but never this one), or
  *could not be read*. The table reads the first 500 devices of a group and says "Showing 500 of N
  devices" when the group is larger. It follows the dashboard's time range, and a paused
  dashboard stops it moving.

Problems with a widget's options, such as an option it does not read, a required one left empty or
a value out of range, are listed in **Version history**. **Publish version** stays disabled until
they are fixed.

Placement problems also keep **Publish version** disabled: a widget outside the sizes it supports,
running past the right edge, or placed relative to another so that it falls off the dashboard. The
editor does not produce these by itself; they come with an imported dashboard, an older widget
release or a dashboard written through the API. **Fit to limits**, in the editor header whenever the
dashboard has any and in **Version history**, clamps every widget into the sizes it supports and
brings any widget that falls off the dashboard back onto it. A widget that a larger size now covers
moves down; the rest stay where they are. Review the result, then save.

### Placing a widget relative to another

A widget normally sits on the grid. A widget can instead be placed **Relative to…** another, for a
badge on a map or a caption under a chart. Select the widget, choose **Relative to…** and click the
widget it should hang from, or move the focus to it and press **Enter**. A picker with nine anchor points appears; choose the corner, edge or
centre of the host that it attaches to.

A relative widget keeps its size, moves whenever its host moves, may overlap it, and is drawn above
the grid. A dashed line joins it to its host. Drag it to shift it away from the anchor in whole
cells, or hold **Shift** to move in quarter cells. Choose **Grid** to put it back on the grid, in the
first free space. A relative widget cannot itself be a host.

## Previewing with synthetic data {#previewing-with-synthetic-data}

**Preview** replaces live data with generated values, so you can check scales, thresholds and layout
before any device reports. A picker beside the button chooses the shape of the data: **Sine wave**,
**Ramp** or **Random walk**. Preview works in both view and edit mode. Choose **Preview** again to go
back to live data.

<GuideShot id="dashboard-preview" alt="The Widget Lab dashboard in preview mode, with the generator picker set to Sine wave and the Preview button highlighted. The chart shows smooth sine curves, the alarm count shows 1 alarm, worst Major, and the cards and gauge show generated values." />

Generated data stays in your browser and is never saved.

## Exporting {#exporting}

**Export** offers four choices.

<GuideShot id="dashboard-export" alt="The Export menu open on the Widget Lab dashboard, offering Download JSON, Copy JSON, Copy as template (unbound) and Clone as new dashboard…." />

- **Download JSON** saves the dashboard as a file.
- **Copy JSON** copies it to the clipboard.
- **Copy as template (unbound)** copies it without its device and entity bindings, so whoever
  imports it picks their own.
- **Clone as new dashboard…** opens the import review with this dashboard, to create a copy under a
  new token.

## Versions {#versions}

**History** opens the version history. A dashboard's draft is what you edit; a **version** is a
frozen copy of the draft that viewers are shown.

<GuideShot id="dashboard-history" alt="The Version history panel. A Publish current draft section has an optional Label field, a Description field, the note that publishing makes the new version the one viewers are shown, and a Publish version button. Below, Published versions lists version 1, marked Live, published by dana.okafor@acme-properties.example, with a Roll back button." />

- **Publish current draft.** Add an optional label, such as a release name, and a description, then
  choose **Publish version**. The new version goes live straight away. Save your changes first: the
  console publishes the saved draft.
- **Published versions** lists each version with when and by whom it was published. The live one is
  marked **Live**.
- **Make live** serves an earlier version to viewers without touching your draft.
- **Roll back** replaces the draft with an earlier version. Anything in the draft that you had not
  published is lost, and the console asks you to confirm.

History only grows. Publishing, making a version live and rolling back each leave the earlier
versions in place.

## Related

- [Dashboards](/concepts/dashboards): widgets, data sources, versioning and embedding
- [Basemaps](/concepts/basemaps), for the map widget's tiles
