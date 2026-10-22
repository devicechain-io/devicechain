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

## Creating a dashboard from a device type {#from-a-device-type}

On a device type's **Profile** tab, **Create dashboard** builds a starting board from what the
profile declares, so you reach a useful board in a minute instead of placing widgets one by one. The
button needs the dashboard write permission and is not offered to a member limited to part of the
tenant.

1. Choose **Create dashboard**. The drawer reads the profile and shows a **Layout preview**.
2. Give the board a name and a token. Beneath the preview, the notes say how many cards, charts and
   buttons it will have and anything that was left out.
3. Choose **Create draft and open editor**. The board is created as a draft, never published, and
   opens in the editor.

The board uses only the widgets you can already add by hand:

- a **device selector**, with the slot set to a device of this type when one exists. The selector
  lists the tenant's devices, not only this type's, so pick a device of the type you built the board
  for. A note beside the selector on the board says so;
- a **latest-value card** for each declared measurement, in the profile's order, with its unit.
  A calculated metric gets a card too, marked **Calculated** in its title, and reads like any other;
- a **chart** of the first few numeric measurements, and an **alarm table** for the selected device;
- a **command button** for each declared command that is safe to send from a board. Commands
  declared disruptive or destructive, or whose impact is unknown, get no button; a note on the board
  says how many were left out.

A profile can declare dozens of measurements. The board shows the first twelve as cards and a note on
the board says how many were left out; add the rest in the editor. A profile that declares no
measurements is refused with an explanation, because there is nothing to build from.

The drawer reads the profile's active published version, which is what the platform checks readings
and commands against, whether or not the type has devices yet. Only a profile that was never
published is read from its draft, and the drawer says so; a measurement the draft names is not
accepted until the profile is published. Profiles do not declare severity bands, so the cards carry
none; add them in the editor.

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

<GuideShot id="dashboard-view" alt="The Widget Lab dashboard in view mode. The header has the time ranges 15m, 1h (selected), 6h, 24h and 7d, a Pause button, a Live indicator, and Preview, Export, History, Edit and Delete buttons. Widgets include a title label, a latest-value card and a gauge for temperature on wl-sensor-01, an Active alarms count for wl-zone-01, an image, a Temperature and humidity chart marked 15m, and a Latest values table listing battery, humidity, pressure and temperature." />

Along the top:

- **15m**, **1h**, **6h**, **24h** and **7d** set the time range the charts draw, and **Pause**
  freezes the dashboard. They work as they do in the /dash viewer; see
  [Time range and pause](/user-guide/viewing-dashboards-in-dash#time-range-and-pause).
- **Live** shows that the dashboard's live connection is up; a device that is merely quiet does not
  change it. If the connection drops it reads **Reconnecting**, then **Offline** once it has been
  down for 20 seconds, or straight away if the browser itself goes offline. The widgets pick up again
  on their own when the connection returns.
- **Preview** swaps live data for generated data (see [Previewing with synthetic data](#previewing-with-synthetic-data)).
- **Export** copies or downloads the dashboard (see [Exporting](#exporting)).
- **History** opens the version history (see [Versions](#versions)).
- **Edit** switches to the editor.
- **Delete** removes the dashboard.

Widgets that carry actions, such as acknowledging an alarm or sending a command, work here too. The
platform checks each action against your own permissions.

### Choosing a device, and sharing a link {#sharing-a-link}

Where a board has a slot that widgets follow (a **Target slot** on a map, a fleet table or an alarm
table, or an entity selector), choosing a device re-points the widgets bound to that slot. On a map,
click a marker, or tab to it and press Enter, to choose that device; set the map's **Target slot**
in the editor to turn this on. Without one, a marker is only a mark.

The address in your browser always reproduces what you are looking at. Choosing a device adds
`bind.<slot>=<device token>` to it, for example:

```text
/dashboards/pump-station?bind.machine=hauler-7
```

Copy the address and send it, and whoever opens it sees the board with that device chosen. The
`<slot>` is the slot's name as it appears in the editor, and the value is the device's token. Several
slots can be set in one link by repeating the parameter.

A link is checked with the permissions of the person who opens it, never those of the person who
sent it. If the device does not exist, or exists but is outside what you are allowed to see, the
page says so instead of showing the board, and offers **Open the board without it**, which opens
the board on its own default device. Both cases read the same on purpose: the platform does not
confirm that a device you may not see exists. A link that names a slot the board does not have, or
something that is not a device token, is refused the same way. So is a link that names the same
slot twice, and one that names a slot whose device follows another slot, since the board would show the device it derives rather than the one in the link. If the check
itself could not be completed, for example because the connection dropped, you can **Try again**.

Choosing a device replaces the address rather than adding a history entry, so **Back** leaves the
board instead of stepping through every device you clicked. The same link works in the /dash viewer;
see [Linking to a board with a device chosen](/user-guide/viewing-dashboards-in-dash#linking-to-a-device).

### What the widgets look like

The test tenant's *Widget Lab* dashboard holds one of every built-in widget. Further down the page:

<GuideShot id="dashboard-gauges" alt="Indicator widgets on wl-sensor-01: a linear gauge and a thermometer for temperature, with their threshold bands marked along the scale, a Tank level gauge filled to its humidity percentage, a Battery icon with its charge, a Status light reading Normal, Signal bars, a Powered light reading Off, and below them a compass and two progress rings." />

The linear and level gauges, battery, status light, signal bars, compass and progress ring each show
one measurement of one device. Thresholds mark their bands along the scale, as on the linear gauge
and the thermometer above, and a reading in any band but the normal one takes that band's color.

<GuideShot id="dashboard-stat-cards" alt="Eight stat cards for wl-sensor-01: Average temperature, Peak temperature (in red), Lowest temperature and Readings, each labeled with its statistic and 1h; then Humidity total, Latest temperature showing its reading labeled Latest with how long ago it arrived, Average, last 5 min labeled Average · 5m, and Peak (smallest)." />

A stat card shows one figure over the dashboard's time range, labeled with the statistic and the
span it covers, such as *Maximum · 1h*. A card with its own **Time span** keeps it, like *Average,
last 5 min* above. Turn on **Show trend** and the card draws the measurement's trend over the
same span under the figure, so "is it rising?" is answered on the card itself. The line is always
the neutral color, whatever band the figure is in, and it is left out when the card is too small
to keep the figure readable.

<GuideShot id="dashboard-bar-pie" alt="Two bar charts of wl-sensor-01's latest temperature, humidity and battery: Latest readings with vertical bars in the data source's order, and Largest first with horizontal bars sorted largest first. Below them, two pie charts of the same readings." />

Bar and pie charts compare a device's latest readings. A bar chart can instead compare one
measurement across the devices of a group (see **One bar per** below). It draws columns in a wide
widget and rows in a tall one.

<GuideShot id="dashboard-fleet-table" alt="The Sensors in the zone fleet table for wl-zone-01: one row per device (wl-sensor-01 and wl-sensor-03) with Temperature (C), sorted by that column, Humidity (%) and Battery (%), each value with its age. Below it, a smaller fleet table and a Temperature by sensor bar chart." />

The fleet table shows one row per device of a group and one column per measurement. Click a column
heading to sort by it. Its settings are described under [Configuring a widget](#configuring-a-widget).

A fleet count card puts a number above that table: how many of a group's devices need attention
(*3 of 21*), or simply how many devices the group holds. Its settings are described under
[Configuring a widget](#configuring-a-widget).

## Editing on the canvas

**Edit** turns the dashboard into a canvas. The buttons change to **Undo**, **Redo**, **Add
widget**, **Paste**, **Preview**, **Export**, **History**, **Save** and **Done**, and a bar of
layout settings appears.

<GuideShot id="dashboard-edit" alt="The Widget Lab dashboard in edit mode. The header shows a Live indicator, Undo and Redo, Add widget, Paste, Preview, Export, History, a disabled Save and Done. A settings bar shows Title Widget Lab, Columns 12, Min cell 56, Gutter 8, Padding 0, Size Fill area, Time range 1h and Density Comfortable. Widgets are outlined on the canvas, each showing its latest reading." />

- **Title** is the dashboard's name.
- **Columns**, **Min cell**, **Gutter** and **Padding** set the grid widgets snap to. The cells
  are square and stretch to fill the width; **Min cell** is the smallest a cell may get, in
  pixels. **Gutter** is the space between cells and **Padding** the margin around the dashboard.
- **Size** decides how the dashboard fills the page: **Fill area**, **Fixed width** or **Fixed
  height**.
- **Time range** is the span the dashboard's charts draw when it opens: 15m, 1h, 6h, 24h or 7d.
  One hour is the default. Viewers can pick another from the header and pause the board; see
  [Viewing dashboards in /dash](/user-guide/viewing-dashboards-in-dash#time-range-and-pause).
- **Density** scales the text and spacing of every widget: **Compact**, **Comfortable** (the
  default) or **Wall display**, for a screen read from across a room. The /dash viewer can override
  it for one display.

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

<GuideShot id="dashboard-add-widget" alt="The Add widget menu open in edit mode, showing its Telemetry group: Timeseries chart 6×4, Latest card 3×2, Gauge 3×3, Table 6×3, Linear gauge 4×3, Level gauge 3×4, Battery 3×2, Status light 2×2, Signal bars 2×2, Compass 4×4, Progress ring 3×3, Stat card 3×3, Bar chart 6×4, Pie chart 4×4 and Fleet table 12×6. Each entry shows its id and version, for example timeseries-chart · v1.0.0, and its size in cells." />

| Group | Widgets |
|---|---|
| Telemetry | Timeseries chart, Latest card, Gauge, Table, Linear gauge, Level gauge, Battery, Status light, Signal bars, Compass, Progress ring, Stat card, Bar chart, Pie chart, Fleet table, Fleet count |
| Static | Label, Image |
| Alarms | Alarm table, Alarm count, Alarm severity |
| Control | Command button, Command slider, Command toggle |
| Navigation | Entity selector, which lets a viewer choose which device or entity the dashboard shows |
| Location | Map, which draws on the tenant's map (see [Tenant settings](/user-guide/tenant-settings)) |

Each entry shows the size, in cells, the widget is added at, such as *4×3*. Scroll the menu for the
groups after **Telemetry**. Choose a widget to add it to the first free space on the canvas, then
configure it.

### Configuring a widget

Select a widget on the canvas to open its settings on the right. **Grid**, **Relative to…**,
**Duplicate**, **Copy** and **Delete** appear in a toolbar just above the widget (below it when
there is no room above), and handles on its edges and corners resize it.

<GuideShot id="dashboard-widget-config" alt="Edit mode with the Temperature latest card selected: Grid, Relative to…, Duplicate, Copy and Delete buttons in a toolbar just above the widget, and resize handles on its edges and corners. The settings panel on the right, titled Latest card, shows Title Temperature, a Context section with Binds to set to Scoped to a parent, Parent slot zone and Strategy Manual pick, and the start of a Data source section." />

The settings depend on the widget. Common ones:

- **Title** shown on the widget.
- **Context**: what the widget is bound to. **Root context** binds it directly to a device or
  entity. **Scoped to a parent** makes it follow another widget's context, for example a device
  inside the building chosen elsewhere on the dashboard. With a parent, **Strategy** decides whether
  it follows the parent's **First member** or a member you choose (**Manual pick**).
- **Data source** picks the device or entity the widget reads. For a widget scoped to a parent, it
  explains that the widget follows the parent. Only the fleet table, the fleet count, a bar chart set to one bar
  per device and a timeseries chart set to one line per device can read a device group; every other telemetry widget shows one device, and bound to a
  group it says so rather than mixing the readings of several devices.
- **Measurement** and display options such as units, range and precision.
- **Time span** (timeseries chart): how much history the chart draws, in seconds, from 60 to
  604800. Leave it empty and the chart follows the board's time range, which viewers can change.
  Set it and the chart keeps that span whatever range a viewer picks.
- **Min** and **Max** (timeseries chart): the ends of the value axis. Leave them empty and the
  chart picks its own range, which starts at zero for readings that never go near it. Set them to
  frame the readings that matter: a 92 °C engine on an axis from 70 to 130 sits where its overheat
  line can be seen above it, not as a flat line along the top of the chart. An end is never a cut
  off: a reading or threshold beyond it widens the axis, so nothing drawn falls off the chart.
- **Mark alarms** and **Mark commands** (timeseries chart): both off by default. **Mark alarms**
  draws the chart device's alarms over the readings: a faint band from the moment an alarm was
  raised to the moment it cleared (to the right edge while it is still active) with a line at the
  raise, colored by severity. An alarm with no severity of its own and the line where it cleared are
  drawn in the neutral color, because color on a board means severity and nothing else. **Mark
  commands** draws a dotted neutral line where each command was sent to the device, named by the
  command and, once known, its outcome (succeeded, failed, timed out, and so on). Hover or tap a
  marker for the same details as text. Under the chart, **Events in this range** opens a list of
  every marker, which a keyboard can reach. The markers follow the board's time range and the
  pause, and read only the chart's own device, for a range of any age. At most 30 events are
  drawn, the newest; the chart says exactly how many more fall in the range ("N more not shown").
  An alarm that cleared and was raised again is drawn from its latest raise only, because the
  platform keeps one record per alarm. On a replayed recording the markers come from the recording if it holds alarms or
  commands, and the chart says "not in this recording" for a channel it does not.
- **Statistic** (stat card): which figure of the span to show: average (the default), minimum,
  maximum, sum, number of readings, or the latest reading. The card prints the statistic and the
  span it covers under the figure, for example "Maximum · 1h". The same **Time span** setting
  pins the card to its own span. A card says why it has no figure (the data source cannot compute
  aggregates, the read failed, or there were no readings in the span) instead of showing a number
  it could not compute. The number of readings of a span with none is 0.
- **Show trend** (stat card): off by default. On, the card draws the measurement's trend over its
  span in a thin neutral line under the figure, one point per bucket of the span's chart, for the
  statistic the card shows (the latest-reading card draws the bucket averages). A gap in the
  readings is a gap in the line, a single point is a dot, and a span with no readings draws
  nothing. The figure is the same number with or without it.
- **Order** (bar chart, pie chart): the measurements in the order the data source lists them, or
  sorted smallest or largest first. A measurement that has not reported yet comes last. A bar chart
  draws columns in a wide widget and rows in a tall one.
- **One bar per** (bar chart): *Measurement* (the default) draws a bar for each measurement of one
  device. *Device* draws a bar for each device of the group the widget is bound to, for the one
  **Measurement** you choose, so the data source must be a device group, not a single device. A
  group too large to draw is cut to its first bars in the chosen order, and the chart says "Showing
  N of M devices".
- **One line per** (timeseries chart): *Measurement* (the default) draws a line for each measurement
  of one device. *Device* draws a line for each device of the group the widget is bound to, for the
  one **Measurement** you choose, over the board's time range (or the chart's own **Time span**), so
  the data source must be a device group. Lines keep the group's order and a device keeps its
  color as readings arrive. The stored history is drawn as the **average of each time bucket**, one
  point per bucket, and the chart labels it (for example "avg per 6m"); readings that arrive live
  after it are drawn as they came. Threshold lines are reference values drawn on the chart: a
  plotted history point is a bucket average, not a single reading, and the chart says so beside the
  lines. Alarm and command markers are not drawn in this mode.
- **Most lines** (timeseries chart, one line per device): the most devices drawn, 5 by default and
  at most 12; the chart's colors repeat after five. Each line costs one stored-history read, and
  the platform reads one device at a time, so keep it small. A group larger than the limit shows
  its first devices and the chart says "Showing N of M devices", so a group is never cut silently.
- **Group by** (alarm table): *Device type* shows a tab strip above the table, an **All** tab and then
  one tab per device type that has an active alarm in the widget's scope, each named by the device
  type's name. A tab's badge counts its **active** alarms and takes the color of the worst severity
  among them; a tab with no active alarm has no badge, and a tab whose worst alarm is indeterminate
  stays neutral. The counts come from one read for the whole table, and a tab's rows are read only
  when you choose it. On **All** the table adds a **Device type** column, which a type's own tab
  leaves out. Your tab stays selected when the data refreshes. The strip scrolls sideways on a
  narrow screen; use the left and right arrow keys, **Home** and **End** to move between tabs, and
  **Enter** or **Space** to choose one. The counts are exact, however many alarms the scope holds.
  A viewer limited to some customers gets tabs only for the devices they can see, and an alarm on a
  device they can no longer see appears under **All** only. **Device type tabs** (alarm table, with *Device type* grouping) lists device type
  tokens to show as tabs, in that order, instead of every type that has an alarm; a type not listed
  appears under **All** only.
- **Alarm severity**: how bad it is, at a glance. A stacked bar shows the widget's active alarms
  split by severity, and under it every severity is listed, most severe first, with its count:
  *Critical*, *Major*, *Minor*, *Warning* and *Indeterminate*. A severity with no alarms stays in the
  list, dimmed, so the list never changes shape as alarms come and go, and has no segment in the
  bar. A severity with alarms takes its severity color; *Indeterminate* stays neutral, and with no
  alarms at all the bar is an empty track. The list is real text ("Critical: 2"), so the counts are
  read aloud by a screen reader and the colors are never the only signal; the bar itself is
  decoration. **State** (active by default) and **Acknowledged** narrow which alarms are counted.
  With no data source the widget counts every alarm you can see, whatever raised it (a device, an
  asset, an area, a customer or a group), so its total matches an alarm count's; bound to a device,
  anchor or group it counts the alarms on the devices it covers. **Device type** counts only the
  alarms on devices of that type, by its token, and the caption says so (*5 alarms on pump
  devices*). The counts are exact, however many alarms there are, and a viewer limited to some
  customers counts only the alarms they can see. A data source that cannot count alarms by
  severity says so instead of showing zeros. In a board replay the widget counts the recorded
  alarms; a recording carries no device types, so a widget set to one says it cannot be shown. The
  widget does not filter a linked alarm table when you click a severity.
- **Fleet table**: one row per device of the group the widget is bound to and one column per
  measurement the data source selects. Set the group on the widget's data source and pick the
  measurements to show. **Columns** gives a heading, unit and precision per measurement;
  **Thresholds** color the cells of one measurement's column (the normal band keeps the table's
  color); **Stale after** marks a reading older than that many seconds as stale, and without it
  the table only shows each reading's age. **Show age when behind** prints a reading's age only
  once it is that many seconds older than the newest reading in the table (or stale), so a fleet
  that reports every few seconds keeps one line a row and the device that went quiet stands out;
  the widget's footer still shows how old the newest reading is. Click a column heading, or press it from the keyboard,
  to sort; **Order by** and **Largest first** set the starting order, and devices with no value
  for the sorted column always come last. **Devices per page** pages a long fleet. **Target slot**
  makes the device name a link that selects that device in the widgets bound to the slot. On a
  narrow board the table becomes a card per device. A cell with no number says why: *no data in
  range*, *does not report this* (the device's published profile does not declare this measurement), or
  *could not be read*. The table reads the first 500 devices of a group and says "Showing 500 of N
  devices" when the group is larger. It follows the dashboard's time range, and a paused
  dashboard stops it moving.
- **Fleet count**: a number for a group of devices, bound like the fleet table. **Count** chooses
  what it counts. *Devices needing attention* (the default) counts the devices whose latest
  reading of the **Measurement** is in an abnormal band of the card's **Thresholds** and shows it
  as *3 of 21*, named after the band when every flagged device is in the same labelled one (a band
  labelled *Low* reads "Low") and otherwise "need attention". The number takes the color of the
  worst band among the flagged devices; with none flagged, or a fleet in its normal band, it stays
  the ordinary text color, so a healthy fleet is calm. A device with no reading of the measurement
  is neither healthy nor flagged: it is counted apart, as "2 not reporting" when it sent nothing
  in the span, including a sensor whose probe died while its other readings keep arriving, and as
  "1 could not be read" when its readings failed to load. A device whose published profile does not
  declare the measurement (a crusher in a count of fuel) is outside the question: it is left out of
  the *of 21* and noted as "1 does not report this". That is decided from the profile, not from
  what the device has sent, and a device whose profile cannot be read, or declares no measurement
  at all, is counted as not reporting rather than left out. While the group's
  readings are still loading, the card shows no number rather than a partial one. *Devices* shows
  how many devices the group holds, whatever they report. **Time span** works as for the fleet
  table; **Stale after** applies to the newest reading in the group, as on other cards. A group larger than the 500 devices read says "Showing 500 of N devices",
  and the attention count covers the devices shown.

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
center of the host that it attaches to.

A relative widget keeps its size, moves whenever its host moves, may overlap it, and is drawn above
the grid. A dashed line joins it to its host. Drag it to shift it away from the anchor in whole
cells, or hold **Shift** to move in quarter cells. Choose **Grid** to put it back on the grid, in the
first free space. A relative widget cannot itself be a host.

### Undo, duplicate and paste {#undo-duplicate-and-paste}

**Undo** and **Redo**, first among the editor's buttons, step back and forward through your edits:
moving, resizing, adding and deleting widgets, their settings and data sources, the layout settings
and **Fit to limits**. **Ctrl+Z** (**Cmd+Z** on a Mac) undoes, and **Ctrl+Shift+Z** or **Ctrl+Y**
(**Cmd+Shift+Z**) redoes. A drag or a resize is one step however far the pointer travels, and typing
into one field is one step until you leave the field. The editor keeps the last 100 steps. While you
are typing in a field, these keys undo and redo the typing in that field instead. A screen reader is
told what each step undid, for example "Undid move of Tank level".

Undo only ever goes back to what the dashboard really was: it can bring back a placement problem
that **Fit to limits** fixed, but never one the dashboard did not have. The steps last until you
leave the dashboard or restore a version. Saving and publishing keep them, so you can undo an edit
you have just saved; the dashboard then has unsaved changes again.

**Duplicate**, on the selected widget, or **Ctrl+D** (**Cmd+D**), adds a copy with the same size,
settings and data source in the first free space it fits.

**Copy**, on the selected widget, or **Ctrl+C** (**Cmd+C**), copies it. **Paste**, in the editor
header, adds the widget last copied in this browser tab to the dashboard you are editing, in the
first free space, so you can copy on one dashboard and paste on another. **Ctrl+V** (**Cmd+V**)
pastes from the clipboard, which also reaches a dashboard open in another tab. A widget type this
console does not have is not pasted, and the console names it. Nor is a widget copied in a newer
version of DeviceChain: the console says so; upgrade it to paste
the widget.

On the same dashboard a copy keeps its data source. On another dashboard it keeps it only where that
dashboard has a slot of the same name bound to the same device or entity: slots are numbered per
dashboard, so the same name on two dashboards usually means two different devices. Otherwise the
widget is pasted without a data source, and the editor says so, naming the widget and the slot it
was bound to, until you choose a data source in its settings. A widget copied in another tenant
never keeps its data source, whatever the dashboard: devices belong to their tenant, so the paste
leaves it without one and says why. A widget placed relative to another is copied onto the grid at
its own size; choose **Relative to…** to attach it again.

## Previewing with synthetic data {#previewing-with-synthetic-data}

**Preview** replaces live data with generated values, so you can check scales, thresholds and layout
before any device reports. A picker beside the button chooses the shape of the data: **Sine wave**,
**Ramp** or **Random walk**. Preview works in both view and edit mode. Choose **Preview** again to go
back to live data.

<GuideShot id="dashboard-preview" alt="The Widget Lab dashboard in preview mode, with the generator picker set to Sine wave and the Preview button highlighted. The chart shows smooth sine curves, the alarm count shows 1 alarm, worst Major, the cards and gauge show generated values, and the table lists a single generated value." />

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

<GuideShot id="dashboard-history" alt="The Version history panel. A Publish current draft section has an optional Label field, a Description field, the note that publishing makes the new version the one viewers are shown, and a Publish version button. Below, Published versions lists version 2, marked Live, and version 1, both published by sam.rivera@widgetlab.example. Version 1 has a Make live button, and each version has a Roll back button." />

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

## Sharing with customers and groups {#sharing-with-customers}

A member whose access is limited to some of your customers, or to groups of devices, sees a
dashboard only once it is shared with one of those customers or groups and published. In the
dashboard list, choose **Share** on a dashboard, pick the **Customers** and the **Groups of
devices**, and choose **Save sharing**. The lists you save replace the ones before, and empty lists
share the dashboard with nobody. You can share a dashboard before it is published; the members it is
shared with see it once it is.

Only groups used as an access scope are offered. If a group is missing, enable it as an access scope
on the group's page, as described in
[Limiting a member to a group of devices](/user-guide/members-roles-and-customer-users#group-scopes).
A group that stops being an access scope keeps its existing shares, but cannot be saved again until
it is one: saving it is refused with a message that a chosen group is no longer an access scope.

{/* TODO(screenshot): dashboard-share, the Share drawer with customers and groups picked. */}

Bind the widgets of a dashboard you share to devices, or to the customers you share it with. A
widget bound to a customer, with **Relationship** set to `assigned` (the relationship a device's
**Assignment** tab creates), shows each customer user that customer's devices, provided the customer
is one of theirs; bound to any other customer, it tells them **You do not have access to this
data**. A widget bound to an asset, an area or a group finds its devices through relationships,
which a member with limited access cannot read, so it shows them **Not available for customer
users** instead of its data. For a dashboard shared with a group, bind its widgets to the group's
devices.

A program does the same with the `shareDashboard` mutation, which needs `dashboard:write`:

```graphql
mutation {
  shareDashboard(
    token: "plant-overview"
    anchors: [{ kind: CUSTOMER, token: "acme" }, { kind: GROUP, token: "north-wing" }]
  ) {
    token
    sharedWith { kind token }
  }
}
```

- The list you send replaces the one before it. An empty list shares the dashboard with nobody.
- Every customer must exist, and every group must exist and be an access scope. If one is not, the
  request fails with `NOT_FOUND` and nothing changes. A group that exists but is not an access scope
  is answered exactly as one that does not exist.
- Such a member lists only the **published** dashboards shared with its customers or groups, and
  opens their published version. It never sees a draft or the version history. Any other dashboard is answered
  as if it did not exist.
- Sharing a dashboard does not share data. Each widget still shows only what the member may see.
- A member whose access is limited cannot share a dashboard, and cannot see who a dashboard is
  shared with.
- Deleting the dashboard removes its shares. Deleting a customer or a group removes the shares that
  name it.

## Related

- [Dashboards](/concepts/dashboards): widgets, data sources, versioning and embedding
- [Basemaps](/concepts/basemaps), for the map widget's tiles
