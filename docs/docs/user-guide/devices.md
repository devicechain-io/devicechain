---
title: Devices
sidebar_position: 3
---

import GuideShot from '@site/src/components/GuideShot';

# Devices

A device is one physical thing that connects to DeviceChain and reports data: a thermostat, a
meter, a tracker. This chapter covers the device list, the ways to add devices (one at a time,
in bulk, or from a CSV file), exporting the list, disabling a device, and every tab of a
device's page.

It is for tenant users and administrators who register and look after devices. Every device
belongs to a device type, so you need at least one device type before you can add a device.
Device types are covered in the [next chapter](/user-guide/device-types-and-profiles).

## The device list

Open **Devices** in the sidebar, then **Devices** in its sub-menu.

<GuideShot id="devices-list" alt="The Devices page. Buttons along the top right read Export CSV, Import, Bulk create and New device. Below is a table with the columns Status, Token, Name, Type, Description and Created. Twelve rows show Online, and the first row, Boiler room thermostat, shows a dash for its status. Every row's type is Building Pulse Thermostat. The footer reads 'Showing 1–13 of 13 results' with Prev and Next buttons." />

The list shows 20 devices per page. Select a row to open that device's page. The columns are:

- **Status**: whether the device is connected right now. **Online** means it is connected or
  has reported recently. **Disconnected** means its connection reported that it went away.
  **Offline** means it has simply not been heard from for a while. A dash means nothing is
  known yet: the device has never reported, or you are not allowed to see its state.
- **Token**: the device's unique id in this tenant. It cannot change.
- **Name**: a readable name. A disabled device has a **Disabled** badge next to its name.
- **Type**: the device type, in the colors and icon the type was given.
- **Description** and **Created**.

How DeviceChain decides whether a device is online is explained in
[Device presence](/concepts/device-presence).

## Adding one device

Choose **New device**.

<GuideShot id="devices-new" alt="The New device panel open over the device list. It has a Device type picker reading 'Select a device type…', a Token field showing {slug} with a regenerate button and the hint 'Pattern: {slug}', a Name field, a Description field and a Create device button." />

1. Choose the **Device type**.
2. Enter a **Token**, or use the button next to the field to generate one from your
   installation's token pattern. The pattern is shown under the field. `{slug}` is filled in
   from the name you enter.
3. Enter a **Name** and, if you like, a **Description**.
4. If the device reports through a LoRaWAN network server or another sender instead of connecting
   itself, add its [aliases](#aliases).
5. Choose **Create device**.

A new device that connects itself cannot report anything until it has a credential. Add one from
its [Credentials](#credentials) tab. A device that reports through an
[inbound integration](/guides/inbound-integrations) needs an alias instead.

### Aliases {#aliases}

An **alias** is the name another system knows the device by, such as its LoRaWAN DevEUI. The
**Aliases** field of the device form holds up to 8, one value per scheme:

1. Choose **Add alias**.
2. Pick the scheme: **LoRaWAN DevEUI** (16 hexadecimal digits), **Network server device id**
   (the device id in The Things Stack), or **Custom scheme** (a name of your own and a value).
3. Type the value. A DevEUI is stored in upper case whichever way you write it.

An alias belongs to one device in the tenant. An inbound integration finds the device a message is
about by its alias. See [Inbound integrations](/guides/inbound-integrations#aliases).

## Creating many devices at once

**Bulk create** makes a batch of devices of the same type, numbered in sequence.

<GuideShot id="devices-bulk" alt="The Bulk create devices panel. It has a Device type picker, Count set to 10 (1–1000) and Start index set to 1, a Token template of device-{n:04d}, a Name template of 'Device {n}', and an empty External ID template with the placeholder VIN-{random}. A Preview box lists device-0001 Device 1 to device-0003 Device 3, an ellipsis, and device-0010 Device 10. The Create 10 devices button is disabled, with the note 'Choose a device type.'" />

- **Device type**: every device in the batch gets this type.
- **Count**: how many devices to create, from 1 to 1000.
- **Start index**: the first number to use.
- **Token template**: must include `{n}`, or `{n:0Wd}` to pad the number with zeros to
  width W. `device-{n:04d}` gives `device-0001`, `device-0002`, and so on.
- **Name template** (optional): may use `{n}` or `{n:0Wd}`.
- **External ID template** (optional): your own business id for each device. It may also use
  `{random}` for a random id per device. When you create more than one device, it must include
  `{n}`, `{n:0Wd}` or `{random}`, so that each device gets a different id.

The **Preview** box shows the first and last tokens and names the batch will get. Check it, then
choose **Create N devices**.

## Importing devices from a CSV file

**Import** creates devices from a CSV file. Use it to load a fleet you already list in a
spreadsheet, or a file your manufacturer supplied.

<GuideShot id="devices-import" alt="The Import devices from CSV panel. A Credentials control offers None (selected) and Factory credentials, with the hint 'Create the devices only. Add credentials afterwards.' Below are the limits 'A CSV file up to 5 MiB and 10000 rows, with a header row. UTF-8; comma, semicolon or tab separated.', a Choose a CSV file button, an 'Or paste the rows' text box with an example header and row, and a Use pasted rows button." />

1. Under **Credentials**, choose **None** to create the devices only, or
   **Factory credentials** to create each device together with the credential your factory
   supplied for it.
2. Choose a CSV file with **Choose a CSV file**, or paste the rows and choose
   **Use pasted rows**. The file needs a header row, and can hold up to 10,000 rows and 5 MiB.
3. Under **Map the columns**, check which column of your file feeds each field. The token column
   must be mapped, and every row needs a device type: map a type column, or choose a
   **Device type for the whole file**. With **Factory credentials**, the credential type and
   credential id columns must be mapped too. A preview shows the first rows as they will be sent.
4. Choose **Check the file**. This is a dry run: the server decides every row exactly as the
   real import would, and creates nothing.
5. Read **What an import would do**. Each row has an outcome: **Created**,
   **Already exists**, **Conflict**, **Invalid** or **Refused**, with details for anything
   that is not created. You can filter the rows by outcome.
6. Choose **Import N devices**, where N is the number of rows the check found would be
   created. A progress bar shows how far it has got.
   **Stop after this batch** stops cleanly between batches. Importing the same file again
   carries on where it stopped.
7. When it finishes, the summary says how many devices were created, how many already
   existed and how many need attention. **Download results** saves every row's outcome, and
   **Download failed rows** saves just the rows to fix. Fix them and import that file. Devices
   that were already created are reported as existing and left alone.

With **Factory credentials**, the file carries secrets. They are sent once, are never shown or
written to the results, and are cleared from the page when the import ends. The file itself is
your responsibility.

The columns, the credential modes and every outcome are described in full in
[Importing devices from CSV](/guides/importing-devices).

## Exporting the list

**Export CSV** downloads every device in the list as a CSV file. The file has the token, name,
type, type token, description and creation time of each device, with times in ISO-8601 format.
It does not include the live status. The headers match what **Import** expects, so an exported
file can be imported again as it is.

While the export runs, the button shows how many rows it has collected, with a
**Cancel export** button. A message confirms how many rows were exported. An export stops at
10,000 rows. If it reaches that limit, the console tells you that the file holds only the first
10,000.

## Disabling and enabling a device

Disabling a device cuts it off without deleting it. Use it to quarantine a device that is
misbehaving or may be compromised, and enable it again when it is fixed.

Open the device and choose **Disable** at the top right. The console asks you to confirm. While
the device is disabled:

- its events are discarded instead of stored, and they do not come back when you enable it;
- it cannot open a new connection, although a connection it already has stays open until it
  ends. When that connection ends, the device shows as disconnected;
- new commands to it are refused;
- commands that were already queued for it are **held**: they are neither sent nor cancelled.

Its data, credentials and configuration are kept. A disabled device has a **Disabled** badge on
its page and in the device list.

When commands are being held, the device's **Commands** tab shows how many, for example
"2 queued commands held while this device is disabled". They go out, oldest first, when you
enable the device, or lapse at their own expiry time. If they should never be sent, choose
**Cancel all**. A command that was already sent cannot be recalled.

To bring the device back, choose **Enable**. Its events are stored again, and it can connect and
receive commands.

The full list of what disabling does, including configuration and alarms, is in
[Disabling a device](/guides/device-credentials#disabling-a-device).

## The device page

Select a device in the list to open its page. The header shows the device's name, its token
(with a copy button), its type and, when it is disabled, a **Disabled** badge. On the right are
**Disable** (or **Enable**) and **Delete**. Deleting a device cannot be undone.

The page is organized in tabs. You may not see all of them: **Credentials**, **Send data** and
**Replacement** need permission to change devices, and **Updates** appears only when your installation runs the
update service and you are allowed to see updates.

### Basic

**Basic** holds the device's type, token, name and description.

<GuideShot id="device-detail-basic" alt="The device page for Lobby thermostat, token bp-therm-001, of type Building Pulse Thermostat. Disable and Delete buttons are at the top right. The tabs are Basic, Assignment, Facets, Connectivity, Events, Commands, Configuration, Updates, Credentials and Replacement. The Basic tab is open, with the Device type, a greyed-out Token field noting 'The device id; it cannot change.', Name and Description, and a Save changes button." />

You can change the type, name and description, then choose **Save changes**. The token cannot
change.

### Assignment

**Assignment** links the device to the customer, area or asset it serves. It organizes the
device's data. It does not decide whether the device may report: a device with no assignment
still reports.

<GuideShot id="device-detail-assignment" alt="The Assignment tab. At the top are a Target type picker set to Customer, a Target picker reading 'Select a target…' and an Assign button. Below, a table lists three assignments with an Unassign button each: asset bp-hvac-01, customer bp-acme and area bp-bldg-01." />

Choose a **Target type** (**Customer**, **Area** or **Asset**), pick the **Target**, and choose
**Assign**. A device can hold several assignments at once. **Unassign** removes one. Events the
device already reported keep the assignments they were recorded with.

See [Managing device assignments](/guides/managing-assignments).

### Facets {#facets}

**Facets** holds the values that describe this device, such as the zone or floor it is on.
**Browse** and dynamic groups find devices by these values.

<GuideShot id="device-detail-facets" alt="The Facets tab. Under 'Facet values' are two facets, Zone (STRING) set to lobby and Floor (LONG) set to 1, each with Save and Clear buttons. Under Zone is the note 'Changing this value moves this entity in or out of: Rooftop units'. Below, 'Other attributes' lists schedule (SHARED, STRING, weekday-standard) and setpoint (SHARED, DOUBLE, 22.5), each with a Delete button, and an Add attribute button." />

- **Facet values** lists the facets your tenant has declared. Enter a value and choose
  **Save**, or **Clear** it. When a value decides membership of a dynamic group, the tab tells
  you which groups a change would move the device in or out of.
- **Other attributes** lists everything else stored on the device. You can add a shared
  attribute with **Add attribute**, and delete one. Values the device reports (**CLIENT**) or the
  platform sets (**SERVER**) are shown for reference only. Shared attributes are also where a
  device's [configuration](#configuration) values live.

Facets and Browse have their own chapter.

### Connectivity

**Connectivity** shows whether the device is connected, and its most recent readings.

<GuideShot id="device-detail-connectivity" alt="The Connectivity tab. The Connectivity section shows an Online badge, Presence source 'Inferred from activity', and the rows Last connect, Last activity and Last disconnect, whose times are hidden in this screenshot. The Latest readings section lists the measurements co2, humidity, setpoint and temperature, with their values and update times hidden." />

- The badge says **Online**, **Disconnected** or **Offline**.
- **Presence source** says how that is known. **Reported by the transport** means the
  connection itself told DeviceChain. **Inferred from activity** means DeviceChain judged it from
  how recently the device sent something.
- **Last connect**, **Last activity** and **Last disconnect** give the times.
- **Latest readings** lists the last value of each measurement, and when it arrived.

When an inferred device has gone quiet, the tab adds a "No recent activity" note: it has not
sent anything for a while, which a healthy device on a slow schedule can also look like.

A device whose profile declares a position, or that has reported one, also gets a
**Last known position** section, with its latitude, longitude and, when reported, elevation,
accuracy, speed and heading. The thermostat in the screenshot does neither, so the section is not
shown.

### Events

**Events** lists the 10 most recent events the device sent.

<GuideShot id="device-detail-events" alt="The Events tab: a table with the columns Occurred, Type and Source. Ten rows each show the type Measurement and the source http1, with the times hidden in this screenshot. Below it reads 'Showing the 10 most recent events.'" />

Each row shows when the event occurred, its type (such as **Measurement**) and the source it
arrived through. For a longer history, use a dashboard.

### Commands {#commands}

**Commands** is where you send a command to this one device, and follow the commands already
sent to it.

<GuideShot id="device-detail-commands" alt="The Commands tab. A Command picker reads 'Select a command', with the hint 'The commands this device's published profile declares.', and an Issue command button that is disabled. Below, a table with the columns Queued, Name, Status and Result shows one command, set-setpoint, with status SENT and no result yet." />

The **Command** picker offers the commands the device's profile declares. Pick one, and the
console shows a form for its parameters.

<GuideShot id="device-commands-parameter-form" alt="The Commands tab with 'Set temperature setpoint (set-setpoint)' chosen. Two parameter fields appear: setpoint, marked required, with the hint 'Target temperature · in Cel · 10–30', and mode, a picker reading 'Select a value' with the hint 'Operating mode'. The Issue command button is now enabled." />

Fill in the parameters and choose **Issue command**. The form checks each value against the
profile, for example the allowed range, before anything is sent. If the profile declares no
commands, you can type any command name and an optional payload instead.

The table below lists the device's commands with their status, such as **QUEUED**, **SENT**
or **SUCCESSFUL**, and the result the device reported. A command that has not reached the device
yet (**QUEUED**, **HELD** or **PARKED**) has a **Cancel** button. A command that has been sent
cannot be cancelled.

What each status means is explained in [Commands](/concepts/commands#command-lifecycle),
and [Sending a command](/guides/sending-commands) covers sending and following one. To send
a command to many devices at once, use a command batch.

### Configuration {#configuration}

**Configuration** shows the settings DeviceChain keeps for the device to apply, and whether
the device has applied them.

<GuideShot id="device-detail-configuration" alt="The Configuration tab. A Pending badge and the text 'Revision #3 is waiting to be applied; the device has not reported a revision.' Below are Desired revision #3 with its sha256 digest, Reported revision 'Never reported', and dashes for Report status, Reported and Last synchronized. A Declared keys table lists setpoint (DOUBLE), schedule (STRING) and reportIntervalSeconds (LONG) with descriptions. 'Undeclared shared attributes' shows floor and zone. At the bottom is the Desired document, a line of JSON with schedule and setpoint." />

A device's configuration is a small document built from its shared attributes. Only the keys
its profile declares are included. Each change makes a new numbered **revision**, which is sent
to the device.

- The badge at the top says whether the device is up to date. **In sync** means it has applied
  the latest revision. **Pending** means a revision is waiting to be applied. **Out of date**
  means the values have changed since the latest revision. A new revision is made when a
  declared key is next written or the device next synchronizes.
  **No revision yet** means nothing has been made.
- **Desired revision** is the latest one, and **Reported revision** is the one the device last
  said it holds. **Report status** is what the device did with it: **Received**, **Applied**
  or **Rejected**. When the device rejects a configuration, its reasons are listed.
- **Declared keys** lists the keys the profile declares, from its active published version.
  You change their values as shared attributes in the [Facets](#facets) tab.
- **Undeclared shared attributes** are shared attributes the profile does not name. They are
  never sent to the device.
- **Desired document** is the exact document the device is sent.
- **Revision history** lists the device's revisions, newest first, with who made each one.

You can put a device back to an earlier configuration, or give it another device's. In both
cases the console shows a preview first, and nothing changes until you choose **Apply**.

To restore an earlier revision, choose **Restore** on its row in **Revision history**.

<GuideShot id="device-configuration-restore" alt="The Configuration tab scrolled down. A 'Restore revision #2' preview reads 'Applying changes 1 key. It creates a new revision and sends it to the device; earlier revisions are kept unchanged.' Its table has the columns Key, Current, After and Change: schedule stays weekday-standard, and setpoint goes from 22.5 to 21, marked Changed. Apply and Cancel buttons follow. Below is the Revision history table with revisions #3 and #2, their digests, creation times and the user who changed them, and a Restore button on #2." />

To copy another device's configuration, pick the device under **Copy from another device** and
choose **Preview changes**. The copy takes that device's latest revision.

<GuideShot id="device-configuration-copy" alt="The Copy from another device section with Rooftop AHU 1 return air chosen. A preview titled 'Copy the configuration of “bp-therm-002”' reads 'Applying changes 3 keys.' Its table shows reportIntervalSeconds added with 300, schedule removed, and setpoint changed from 22.5 to 19.5. Apply and Cancel buttons follow." />

The preview lists every key with its **Current** value, the value it will have **After**, and
whether it is **Added**, **Changed** or **Removed**. Applying makes a new revision and sends it to
the device. Earlier revisions are kept unchanged. The change is refused if this device's profile
does not declare every key the configuration carries.

How a device receives and reports its configuration is described in
[Restoring or copying a configuration](/guides/connecting-a-device#restore-copy-configuration).

### Updates

**Updates** shows the over-the-air update assigned to the device, and its update history. It is
covered in the over-the-air updates chapter.

### Credentials {#credentials}

**Credentials** holds what the device uses to prove who it is when it connects.

<GuideShot id="device-detail-credentials" alt="The Credentials tab for Rooftop AHU 2 return air. At the top, a form with a Type picker set to ACCESS_TOKEN, an Access token field with a Generate button, and an Add credential button. Below, a table lists two credentials: MQTT_BASIC with the id rooftop-ahu-2 and the note 'Password is set. It cannot be shown.', enabled, with Set new password and Delete buttons; and ACCESS_TOKEN, whose id is hidden in this screenshot, enabled, with a Delete button." />

There are two types:

- **ACCESS_TOKEN**: the device presents a token. Paste your own or choose **Generate**.
- **MQTT_BASIC**: the device connects with a username and password.

Choose the **Type**, fill in the fields and choose **Add credential**. A device can hold more
than one credential. **Delete** removes one, after which the device can no longer authenticate
with it.

An MQTT password is shown only once: right after you add the credential, or after you choose
**Set new password** to change it. The platform keeps only a one-way digest, so it can never
show the password again.

<GuideShot id="device-credentials-shown-once" alt="The Credentials tab with a highlighted box titled 'New password set'. It reads 'Copy it now. You won't see this password again; the old one no longer works.', then shows the username rooftop-ahu-2 and the new password with a copy button, and a Done button." />

Copy the password and give it to the device, then choose **Done**. When you set a new password,
the old one stops working as soon as you save.

See [Device credentials](/guides/device-credentials).

### Send data {#send-data}

**Send data** builds a ready-to-run command that sends the device its first measurement: a
`mosquitto_pub` for MQTT, or a `curl` for HTTP. Choose the transport, and the credential if the
device has more than one, then copy the command with the copy button.

The command uses the device's token and credential, and the metrics in the **published** version
of its profile, with example values the profile accepts. A metric that exists only in the draft is
not used, because the platform checks readings against the published version. When the published
version has no metric the example can use, the command sends an undeclared reading instead, which
is stored as long as its value is a number. At most eight metrics are included.

The commands are written for a POSIX shell such as bash or zsh. On Windows, run them in WSL or
Git Bash: PowerShell and `cmd.exe` read the quoting and the line breaks differently, so the
command would not send what it shows.

The console cannot know some values, so the command leaves a placeholder for each:

| Placeholder | Replace it with |
| --- | --- |
| `<instance-id>` | The instance id chosen when the instance was installed. It is the instance's Kubernetes namespace without the `dci-` prefix. |
| `<mqtt-host>` | The host the MQTT listener is exposed on. The listener uses TLS, so save the instance CA certificate as `instance-ca.crt`. |
| `<ingest-host>` | The host of the HTTP ingest listener, port 8081. A default install does not expose it outside the cluster, apart from the inbound-integration webhook at `/api/integrations/{tenant}/{integration}` on the instance's HTTPS address; see [Your First Device](/quickstart/first-device#5-open-a-path-to-the-ingest-endpoint). |
| `<password>` | The password of an `MQTT_BASIC` credential. It is shown only once, when it is set. If it is lost, set a new one on the **Credentials** tab. |

The tab offers no command when the command could not work, and says what to fix instead:

- The device is **disabled**. A disabled device's events are discarded when they arrive.
- The device has no enabled, unexpired credential. By default the platform requires a credential
  on every connection and every event. Add one on the **Credentials** tab first.

A device that is not assigned to anything can send data; its events are stored.

Like **Credentials**, the tab needs permission to change devices, because an access token is
written into the command.

A `202` from HTTP, or an MQTT acknowledgement, means the event was received, not that it was
stored. Look for it on the **Events** tab.

See [Connecting a Device](/guides/connecting-a-device).

### Replacement

**Replacement** is for swapping the hardware: a unit has failed and a new one takes its place.
The device keeps its identity, history, alarms, assignments and group membership. Only the
credentials change.

<GuideShot id="device-detail-replacement" alt="The Replacement tab for Boiler room thermostat. The explanation reads 'Bind a new physical unit to this device. Its identity, external id, type, history, alarms, assignments and group membership all carry forward — only the credentials change.' The Reason field holds 'Display unreadable after a power surge' and the New unit identifier field holds SN-5180-C, above a Replace unit button. Below, a history table with the columns Replaced, By, Reason, New unit and Credentials retired shows one earlier replacement, with its time hidden in this screenshot: by dana.okafor@acme-properties.example, reason 'Failed sensor, swapped for the stockroom spare', new unit SN-4471-B, 1 credential retired." />

1. Enter the **Reason**. It is recorded on the maintenance history.
2. Enter the **New unit identifier**: the new hardware's own serial number or IMEI. It is recorded
   on the history. The device's external id does not change.
3. Choose **Replace unit** and confirm.

Every credential the old unit could use is disabled, and a new credential is issued for the new
unit. The new credential is shown once, so copy it before you choose **Done**. It cannot be read
back from the history.

The table below lists earlier replacements: when, by whom, why, the new unit and how many
credentials were retired.

## Related

- [Device types and profiles](/user-guide/device-types-and-profiles)
- [Domain model](/concepts/domain-model)
- [Device presence](/concepts/device-presence)
- [Commands](/concepts/commands)
- [Importing devices from CSV](/guides/importing-devices)
- [Device credentials](/guides/device-credentials)
- [Managing device assignments](/guides/managing-assignments)
- [Connecting a device](/guides/connecting-a-device)
- [Inbound integrations](/guides/inbound-integrations)
