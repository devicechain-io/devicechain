---
sidebar_position: 3.5
title: Importing Devices from CSV
---

# Importing Devices from CSV {#importing-devices}

Operators usually onboard a fleet from an inventory file: a serial number, a name, a device type, sometimes a factory-issued credential. **Import** reads that file and creates the devices in it. It is the door for an arbitrary inventory; for "N numbered test devices" use **Bulk create**, which renders devices from templates.

:::note Status
Available in the console (**Devices → Import**) and over the device-management GraphQL API as `importDevices`.
:::

Import is **create-only** and **safe to run again**. The device token is the key: a row whose device already exists exactly as the row describes it is reported as `EXISTS`, a row whose device exists differently is reported as `CONFLICT`, and an existing device is never changed. Each row is its own transaction, so one bad row never stops the others.

## Import in the console {#console}

1. Open **Devices** and choose **Import**.
2. Choose how credentials are handled ([modes](#credential-modes) below).
3. Choose a CSV file, or paste the rows. The file needs a header row.
4. Check the column mapping. Columns whose header matches a field are mapped for you, including the headers the console's own **Export CSV** writes, so an exported list can be imported as it is. Choose a device type for the whole file if the file has no type column.
5. Choose **Check the file**. This is a dry run: the server decides every row exactly as the real import will and writes nothing. The results list each row's outcome and can be filtered by outcome.
6. Choose **Import N devices** (N is the number of rows the check found would be created). The rows are sent in batches of 1000, one after another, with progress; **Stop after this batch** ends the run cleanly between batches.
7. When it finishes, download the **results** (every row's outcome) or the **failed rows**, fix them, and import that file. Devices that were already created report `EXISTS` and are left alone.

A file may hold up to 10,000 rows and 5 MiB. A larger file is refused before it is read; split it and import the parts.

### Reading the file {#reading-the-file}

- The file must be UTF-8. A file saved in another encoding is refused with a message, not read with garbled characters; in a spreadsheet, save as **CSV UTF-8**. A leading byte-order mark is ignored.
- Commas, semicolons and tabs are recognised from the first line, so a file saved by a spreadsheet in a locale that uses decimal commas imports as it is.
- Values may be quoted, may contain the delimiter, doubled quotation marks and line breaks, and lines may end in `LF`, `CRLF` or `CR`. Blank lines are skipped.
- Nothing in the file is evaluated. A cell that starts with `=`, `+`, `-` or `@` is stored as the text it is. (The console's export adds a leading apostrophe to such cells so a spreadsheet does not treat them as formulas; import removes that apostrophe again, so a round trip returns the original values.)
- In the console, surrounding spaces are trimmed from every value except the credential secret, which is sent exactly as written.

## Columns {#columns}

| Column | Required | Meaning |
| --- | --- | --- |
| `token` | Yes | The device's token: letters, digits, hyphens and underscores, starting with a letter or digit, up to 128 characters. It is the key a re-run matches on. |
| `deviceTypeToken` | Yes, per row or for the whole file | The token of an existing device type. An unknown type makes the row `INVALID`. |
| `externalId` | No | Your business identifier, up to 256 characters, unique in the tenant. It is checked for conflicts and is never the key. |
| `name` | No | Up to 128 characters. |
| `description` | No | Up to 1024 characters. |
| `metadata` | No | A JSON document, up to 64 KiB. Compared by value on a re-run, so key order and spacing do not matter. |
| `provisioningProfileToken` | No | The token of a [provisioning profile](./device-self-registration.md#earmarking) to earmark the device for, so that profile can adopt it when the device registers itself. It must exist and be of the device's type, or the row is `INVALID`. A device that already exists makes the row a `CONFLICT` whenever its earmark differs from the one the row names, including a device with no earmark: an import never updates an existing device, so it cannot earmark one. |
| `credentialType` | Factory credentials only | `ACCESS_TOKEN` or `MQTT_BASIC`. |
| `credentialId` | Factory credentials only | What the device presents: the access token itself, or the MQTT username. |
| `credentialValue` | `MQTT_BASIC` only | The password, at least 16 bytes, at most 4096. Write-only. |

Devices are always created **enabled**; there is no column for disabling a device on import. Disable one afterwards from its detail page.

## Credential modes {#credential-modes}

| Mode | What it does | Secrets in the file |
| --- | --- | --- |
| **None** (`NONE`) | Creates the devices only. Credentials are added afterwards, from the device's **Credentials** tab or over GraphQL. | No |
| **Factory credentials** (`FACTORY_CREDENTIALS`) | Creates each device together with the credential your factory supplied for it, in one transaction. A row never ends up with a device and no credential, or the reverse. | Yes: the file already exists at your factory; import consumes it and creates none |

There is no mode that generates credentials and hands you a file of secrets. DeviceChain does not author a secret-bearing file.

Credential columns are refused outside factory-credentials mode, so a secret cannot ride along in a file by accident.

A pre-provisioned mode, for fleets that provision themselves, arrives with self-service provisioning.

### How secrets are handled {#secrets}

`credentialValue` is **write-only**. It is stored only as a keyed digest, exactly as when you register a credential on its own, and it appears in no outcome, no message, no log line and no results file. The **failed rows** file never contains a secret column, so a corrected file needs its secrets added back from your own source. In the console the secrets are held in the page only while the import runs and are cleared when it ends. The file itself remains your responsibility.

On a re-run, a row whose device and credential id already exist is `EXISTS`; the secret is **not compared**, so a changed password in a re-run changes nothing. To rotate a password, use the credential's own update.

## Outcomes {#outcomes}

| Outcome | Meaning |
| --- | --- |
| `CREATED` | The device (and its credential) was created. In a dry run: would be. |
| `EXISTS` | The device already exists exactly as the row describes it. A success; nothing was written. |
| `CONFLICT` | The token exists with different `deviceTypeToken`, `externalId`, `name`, `description` or `metadata`; or the external id or credential id belongs to another device; or the device exists without the credential the row names. Nothing was written. |
| `INVALID` | The row is malformed or names something that does not exist: a bad token, an unknown device type, a value over its limit, invalid JSON, a repeated token, external id or credential id elsewhere in the same file, or a credential problem. |
| `REFUSED` | The row is well-formed and a rule refused it: a credential type that is not supported. |

Every outcome other than `CREATED` and `EXISTS` carries a `code` and a `message` that name the field. A message never contains a credential secret; it may repeat a token you supplied, such as a device type token. An import never adds a credential to a device that already exists; use the credential's own create for that.

## Over GraphQL {#graphql}

```graphql
mutation Import($rows: [DeviceImportRow!]!) {
  importDevices(rows: $rows, mode: NONE, dryRun: true) {
    created exists conflict invalid refused
    rows { row token outcome code message }
  }
}
```

```json
{ "rows": [
  { "token": "sensor-001", "deviceTypeToken": "thermostat", "name": "Boiler room" },
  { "token": "sensor-002", "deviceTypeToken": "thermostat", "externalId": "SN-20413" }
] }
```

- A call takes **at most 1000 rows**; more fails the whole call with `LIMIT_EXCEEDED`. Send a larger file as several calls, one after another.
- `row` is the row's position in the call, starting at 1.
- `dryRun: true` decides every row by the same code the real call uses and writes nothing, so a clean dry run is a faithful preview.
- A failure that is not about a row (the database becoming unavailable) fails the call with an error. Rows created before it are real; send the same rows again and they report `EXISTS`.
- Requires the `device:write` authority, the same as creating a device.
