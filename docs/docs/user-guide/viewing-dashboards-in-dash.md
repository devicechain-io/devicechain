---
title: Viewing dashboards in /dash
sidebar_position: 17
---

import GuideShot from '@site/src/components/GuideShot';

# Viewing dashboards in /dash

Besides the console, the instance serves a separate dashboard viewer at `/dash/`. It shows a
published dashboard and nothing else: no sidebar, no editor, no draft. Use it for a wall display in a
control room, for a bookmark that opens straight onto one board, or for people who only need to look
at a dashboard. This chapter is for anyone who opens it, and for whoever sets up a wall display.

Dashboards themselves are built and published in the console; see [Dashboards](/user-guide/dashboards).

## Signing in

The viewer has its own sign-in form, with **Email**, **Password**, **Sign in** and **Sign in with
SSO**, and a language switch. For single sign-on, type your email address first, then choose **Sign
in with SSO**. If your account belongs to several tenants, the viewer asks you to choose one.

<GuideShot id="dash-signin" alt="The /dash sign-in card with an Email field holding sam.rivera@widgetlab.example, an empty Password field, a Sign in button, a Sign in with SSO button, and EN English and ES Español language buttons." />

The viewer keeps its session in the page's memory only. It does not share the console's session:
being signed in to the console does not sign you in here, and signing out of one leaves the other
alone. Reloading the page, or opening `/dash/` in another tab, asks you to sign in again.

## Loading a dashboard

After you sign in, **Load a dashboard** lists **Your dashboards**: the published dashboards you may
open. Choose one to open it. If your access is limited to some of the tenant's customers or groups
of devices, the list holds the published dashboards shared with those customers or groups;
otherwise it holds every published dashboard in the tenant. The list shows the first 200, and says so when there are more.

{/* TODO(screenshot): dash-list, the Your dashboards list above the token field. */}

Below the list, you can also open a dashboard by its **Dashboard token**, which you can copy from its
page in the console: type it and choose **Load published**. The viewer fetches the version that was
last published. A dashboard that has never been published cannot be opened here; the viewer asks
you to have an author publish it.

<GuideShot id="dash-load" alt="The Load a dashboard page. The Dashboard token field holds wl-gallery, with the hint Fetches the version of this dashboard that was last published, and a Load published button. Below, under Or paste an exported definition, are a Definition (JSON) box, a Binding manifest (JSON, optional) box with a sample of its shape, and a Render button. The header has the language buttons and Sign out." />

The lower half takes a dashboard you exported from the console instead: paste the definition and,
optionally, a binding manifest that points its slots at particular devices, then choose **Render**.
See [Exporting](/user-guide/dashboards#exporting) and, for binding manifests,
[Embedding](/concepts/dashboards#embedding-definitions-slots-and-binding-manifests).

## The board

The loaded board fills the page under a slim header. The header shows the dashboard's title, the
time range and **Pause** (see [Time range and pause](#time-range-and-pause)) and a connection
indicator, and offers the language switch, **Dark theme** (or **Light theme**), **Change**, which
returns to the load form, and **Sign out**.

<GuideShot id="dash-view" alt="The Widget Lab board in the /dash viewer, light theme. The header shows Widget Lab, the time ranges 15m, 1h (selected), 6h, 24h and 7d, a Pause button, a Live indicator, and the buttons English, Español, Dark theme, Change and Sign out. Below: a title card, a temperature readout, a temperature gauge, an Active alarms count, an image, a line chart of temperature and humidity marked 15m, and a Latest values table." />

The widgets are live: values, charts and alarms update as data arrives. The connection indicator
reads **Live** while the board's live connection is up; a device that is merely quiet does not change
it. If the connection drops it reads **Reconnecting**, then **Offline** once it has been down for 20
seconds, or straight away if the browser itself goes offline, and the widgets pick up again on their
own when it returns. The viewer cannot change the
dashboard, but widget actions still work for those allowed to use them. Someone whose role grants
`alarm:write` can acknowledge and clear alarms from the board, and someone with `command:write` can
send commands to real devices. The server checks those rights for every action. See
[Actions and permissions](/concepts/dashboards#actions-and-permissions).

Dashboard and widget titles are text the dashboard's author wrote, so they stay as written whatever
language the viewer is set to.

### Light and dark

The theme button switches the board between light and dark, and this browser remembers your choice
for next time.

<GuideShot id="dash-dark" alt="The same Widget Lab board in the dark theme: dark backgrounds, light text, the header button now reading Light theme, and the readout, gauge, alarm count, chart and table restyled for the dark background." />

## Time range and pause {#time-range-and-pause}

The header carries a time range: **15m**, **1h**, **6h**, **24h** and **7d**. It sets how much
history the charts draw, counted back from now. The board opens on the span its author chose, one
hour if they chose none, and the span you pick applies to every chart that follows the board's
range. A chart whose author pinned its own span keeps that span, and shows it in its corner (for
example **1h**) whenever it differs from the range in the header, so a pinned hour never reads as a
day of data. Long ranges are drawn from averaged points rather than every reading, so a week stays
quick to load.

A chart holds at most 2,000 readings. On a long range over a busy device that can be fewer than the
whole range: the oldest readings are let go first, and the chart then says where it starts (for
example **from 14:03**) in its corner, so a partial window is never mistaken for the full range.

**Pause** freezes the board. Values, charts, alarm counts, alarm tables and maps stop where they
are, and a **Paused** badge with the time you paused appears next to the controls. Time keeps
passing on a frozen board: the age beside a value ("12 sec. ago") keeps counting from the reading
you are looking at, so a paused value never looks current. Choose **Resume** and the board catches
up with everything that arrived in the meantime. While paused you can still change the range; the
board shows that span as it stood at the moment you paused.

Pause freezes what the board shows, not what it does. Command buttons keep working while paused,
and so does the list of recent commands they show, which is why the **Paused** badge also reads
"commands still send": a command sent from a frozen board goes out at once.

## Kiosk mode and links for wall displays

The viewer's address can carry parameters, so a bookmark or a wall display opens the right board in
the right mode:

```text
/dash/?dashboard=<token>&kiosk=1&theme=dark
```

| Parameter | Values | Effect |
| --- | --- | --- |
| `dashboard` | a dashboard token | Loads that dashboard straight after sign-in, including after single sign-on. |
| `kiosk` | `1` or `true` | Kiosk mode: hides the header and its controls, and shows a **Fullscreen** button instead. |
| `chrome` | `0` or `false` | With `kiosk`, also hides the connection indicator and the **Fullscreen** button, for a page that frames the board itself. The indicator still appears whenever the board is not live. |
| `theme` | `dark` or `light` | Sets the theme. Unlike the theme button, the parameter is not remembered. |
| `density` | `compact`, `comfortable` or `wall` | Scales the board's text and spacing: `compact` is smaller, `wall` is larger, for a display read from across a room. Overrides the density the board was saved with. |
| `range` | `15m`, `1h`, `6h`, `24h` or `7d` | Opens the board on that time range instead of the board's own. |
| `paused` | `1` | Opens the board frozen at the moment it loads, with the **Paused** badge showing. Any other value opens it live. |
| `bind.<slot>` | a device token | Opens the board with that device chosen for the slot. See [Linking to a board with a device chosen](#linking-to-a-device). |

A parameter with a value the viewer does not recognize is ignored, and the other parameters still
apply.

In kiosk mode the board takes the whole page. The connection indicator sits in the top left corner.
The only control is **Fullscreen** in the top right corner, which turns into **Exit fullscreen**; it
is hidden if the browser cannot go full screen.
There is no sign-out control in kiosk mode, so a wall display stays on its board. Kiosk mode has no
time-range or pause controls either, but a board opened with `paused=1` still shows its **Paused**
badge beside the connection indicator, so a frozen wall display says so.

<GuideShot id="dash-kiosk" alt="The Widget Lab board in kiosk mode: no header, a Live indicator in the top left corner, a Fullscreen button in the top right corner, and the title card and the same live widgets below." />

## Linking to a board with a device chosen {#linking-to-a-device}

A link can also open a board with a device already chosen, by naming the board's slot and the
device's token:

```text
/dash/?dashboard=<token>&bind.machine=hauler-7
```

Repeat `bind.<slot>` for each slot you want to set. The `<slot>` is the slot's name as the board's
author gave it. The viewer shows the board only after it has checked, with your own permissions, that
every device in the link exists and is one you may see. Otherwise it shows why instead of the board:
a device that does not exist and a device you may not see read the same, so a link cannot be used to
find out which devices exist. **Open the board without it** then opens the board on its own default
device. A link that names a slot the board does not have, or something that is not a device token, is
refused the same way. So is a link that names the same slot twice, and one that names a slot whose device follows another
slot, since the board would show the device it derives rather than the one in the link.

Choosing a device on the board (clicking a marker on a map, a row in a fleet table, or a selector)
updates the address in place, without adding browser history entries, so the address in the bar is
always a link to what is on screen. Following **Back** or **Forward** to an address that names another
device chooses that device. **Change**, which returns to the load form, drops the chosen devices from
the address. The same link works in the console, at `/dashboards/<token>?bind.<slot>=<device token>`;
see [Choosing a device, and sharing a link](/user-guide/dashboards#sharing-a-link).

## Staying signed in

A wall display can run unattended for days, so the viewer renews its session on its own, a couple of
minutes before it would expire. A network blip, such as a Wi-Fi reconnect, does not end the session:
the viewer keeps retrying until its current access lapses.

If the session cannot be renewed, because the sign-in was ended on the server or the outage lasted
too long, the board is replaced by a full-screen **Your session has ended** notice with a **Sign in
again** button. The display never sits frozen on old values. When the address carries a `dashboard`
parameter, signing in again brings the same board straight back.

More detail is in [Session refresh and kiosk mode](/concepts/dashboards#session-refresh-and-kiosk-mode).

## Related

- [Dashboards](/user-guide/dashboards): building, publishing and exporting dashboards in the console.
- [Dashboards](/concepts/dashboards): widgets, data sources, versioning and embedding.
- [Signing in](/user-guide/signing-in): the console's own sign-in.
