---
sidebar_position: 16
title: Product as Code
---

# Product as Code {#product-as-code}

You can keep a tenant's product in git. `dcctl product export` writes the device types, device
profiles, dashboards, outbound connectors and inbound integrations of a tenant to a directory of
files. `dcctl product plan` shows what applying those files would change, and `dcctl product apply`
makes a tenant hold them. Use it to promote a product from a development tenant to production, to
review a change in a pull request, or to catch a tenant drifting from what is committed.

:::caution Preview
The command tree and the bundle format (`apiVersion: product.devicechain.io/v1alpha1`) are a
preview. They are new, and a later minor release may change the format. `dcctl` reads only the
`apiVersion` it knows and refuses any other, so a bundle is never half-understood.
:::

The commands talk to the same tenant GraphQL API as the console, as the identity you sign in with.
The server authorizes every call, so you need the permissions the console would need to make the
same changes.

## Export, plan, apply {#workflow}

```
dcctl product export ./acme --tenant acme --email you@example.com --password '<password>'
git add acme && git commit -m "Snapshot the acme product"

dcctl product plan  ./acme --tenant acme-prod --email you@example.com --password '<password>'
dcctl product apply ./acme --tenant acme-prod --email you@example.com --password '<password>'
```

`export` writes to a directory that is empty, absent, or already a bundle (it holds a
`product.yaml`). Re-exporting into a bundle rewrites its files and removes the files of resources
that no longer exist. Exporting twice with nothing changed in between writes the same bytes both
times, so `git diff` shows only real changes. Pass `--name` to label the product in `product.yaml`.

`plan` reads the tenant and prints one line per change, then writes nothing:

```
+ DeviceProfile    thermostat  create
~ Metric           thermostat/m-temp-c  update: unit
- Command          thermostat/c-old  delete (not in the bundle)
^ DeviceProfile    thermostat  publish (the draft changed)
~ Dashboard        overview  update: definition
5 change(s).
```

A line starts with `+` (create), `~` (update), `-` (delete) or `^` (publish). Against a tenant that
already holds the bundle, `plan` prints `No changes`. Add `--fail-on-changes` to exit non-zero when
there is any change, which turns the plan into a drift check in CI.

`apply` builds the same plan, prints it, and runs it. Pass `--yes` to skip printing the plan first.
It is an upsert by token, so running it twice is the same as running it once, and a run that fails
part-way is finished by running it again.

Pass `--kinds` to limit a command to some of `device-types`, `profiles`, `integrations`,
`connectors` and `dashboards`. Without it, `export` skips the connectors with a warning on an
instance that does not run the outbound connectors area, and `plan` and `apply` read only the kinds
the bundle carries.

## What a bundle holds {#layout}

```
product.yaml                 the manifest: apiVersion, kind, an optional name
device-types/<token>.yaml
profiles/<token>.yaml        a profile with all of its definitions
integrations/<token>.yaml
connectors/<token>.yaml
dashboards/<token>.json
```

Every file is named for the token inside it, and every file carries a `kind`. Reading is strict: an
unknown field, an unknown directory, a file whose name is not its token, or a duplicate token is an
error that names the file. Keys are written in alphabetical order, and every JSON document inside a
file is written in canonical form (sorted keys, no insignificant whitespace), whatever the server
returned.

### Device types

```yaml
kind: DeviceType
token: thermo-v1
name: Thermo v1
profile: thermostat          # the token of the profile the type adopts
manufacturer: Acme
model: T1
maintenanceWindow: night     # a reference, see "What is not in a bundle"
metadata: {sku: T-1}
```

### Profiles

A profile file holds the profile and everything authored on it.

```yaml
kind: DeviceProfile
token: thermostat
name: Thermostat
category: hvac
location: {expectedAccuracyMeters: 5.5, expectedUpdateIntervalSeconds: 60}
configurationDeclaration:
  - {key: setpoint, valueType: DOUBLE}
decoder:                      # the payload decoder document, including its test vectors
  kind: CEL
  envVersion: 1
  input: FRAME
  vectors: [...]
metrics:
  - {token: m-temp-c, key: temp-c, dataType: DOUBLE, unit: Cel, min: -40, max: 85}
calculatedMetrics:
  - {token: m-temp-f, key: temp-f, dataType: DOUBLE, expression: temp-c * 9 / 5 + 32}
commands:
  - token: c-reboot
    key: reboot
    impact: DISRUPTIVE
    parameterSchema: [{name: delay, dataType: INT}]
rules:
  - token: r-overheat
    enabled: true
    definition: {...}         # the detection rule document
    authoringGraph: {...}     # the visual canvas it was authored with, if any
```

The definition tokens (`metrics`, `calculatedMetrics`, `commands`, `rules`) are tenant-wide: a token
names one definition wherever it sits. The metric `key` is the key a measurement uses, and is unique
within the profile.

### Dashboards

A dashboard file is JSON. The definition is the same opaque document the console edits.

```json
{
  "kind": "Dashboard",
  "token": "overview",
  "name": "Overview",
  "definition": { "widgets": [], "layout": { "cols": 12 } }
}
```

### Connectors and integrations

```yaml
kind: Connector
token: audit-sink
type: mqtt
config: {urls: ["tcp://broker.example.com:1883"], topic: alerts, qos: 1}
credential:
  handle: env:DC_CONNECTOR_AUDIT_SINK
```

```yaml
kind: Integration
token: ttn
type: TTS_V3
enabled: true
identity: DEV_EUI
deviceTypes: [thermo-v1]
linkMetrics: true
```

## Secrets are handles {#secrets}

A bundle never contains a secret value, and that is enforced from three sides.

- The platform does not reveal a stored connector credential, only whether one is set, and it keeps
  an integration's bearer token only as a digest. An export has nothing to write.
- In place of a value, a connector names its credential by a **handle**: `env:NAME` means "the
  environment variable `NAME` when `apply` runs". `export` writes `env:DC_CONNECTOR_<TOKEN>` for a
  connector that holds a credential. Any other scheme is refused.
- Every bundle is scanned for values shaped like a secret: a value under a key such as `password`,
  `secret` or `apiKey`, a private key block, a cloud access key id, a web token, a bearer
  credential, or a URL with a password in it. `export` refuses to write, and `plan` and `apply`
  refuse to read, a bundle with a finding. The message names the file and the rule and never prints
  the value. `--allow-suspected-secrets` on `plan` and `apply` waives the scan for a value you know
  is not one.

`apply` sends a connector's credential only to a connector that has none. The platform cannot tell
you what is stored, so it cannot tell whether the variable has changed; pass `--rotate-secrets` to
send it to every connector whose variable is set. A connector that needs a credential, and whose
variable is not set, blocks the plan. Removing `credential` from a connector file deletes the
stored credential.

An inbound integration's bearer token is generated by the server when the integration is created.
`apply` does not request it, so it is never received, printed or stored. Rotate the integration's
token in the console to issue one.

## How apply converges {#apply}

Apply runs in dependency order: connectors, profiles with their definitions, device types,
integrations, then dashboards. Within a profile it removes definitions the bundle no longer lists,
then updates, then creates, so a key freed by a removal can be reused.

- **Drafts, then publish.** A profile, connector or dashboard is converged as a draft and then
  published. Apply publishes only when something changed, or the resource has never been published.
  For a profile it also publishes when the active version's metrics, commands, decoder or
  configuration declaration differ from the bundle. A published version cannot be read back with
  its rules or position declaration, so a rule or position change made outside `apply` does not by
  itself trigger a publish. `--no-publish` converges drafts and stops. Published versions carry the
  label `dcctl product apply`.
- **Only what the bundle names.** A device type, profile, integration, connector or dashboard that
  is in the tenant and not in the bundle is left alone, and `plan` lists it as a note. The one
  exception is the definitions under a profile the bundle carries, which converge to the bundle's:
  `apply` deletes a draft metric, command or rule that is not listed.
- **Moves.** A definition whose token is wanted under a different profile is moved by an update, not
  deleted and recreated.
- **Things that cannot change.** An integration's type, and whether a metric is measured or
  calculated, are fixed when it is created. A bundle that disagrees with the tenant on either is a
  blocker, and `apply` refuses the whole plan before writing anything.

## Concurrent edits {#concurrency}

| Kind | Protection while applying |
| --- | --- |
| Connector | Updates and publishes are conditional on the version stamp read when the plan was made. A connector changed by someone else in between is refused with `modified by another writer`, not overwritten. |
| Dashboard | The same: updates and publishes carry the stamp. |
| Device type, profile and its definitions, integration | None. These carry no version stamp, so a concurrent edit is last-writer-wins. |

Run `plan` and `apply` close together, and re-run `plan` after a refusal.

## What is not in a bundle {#limits}

- Maintenance windows, dynamic groups, customers, geofences, assets, devices, credentials and
  notification channels are not part of a bundle. A device type's `maintenanceWindow` and a rule's
  `entityGroupToken` are references by token; the tenant you apply to must already hold them, or the
  server refuses the write.
- A dashboard's sharing with customers and groups is not carried.
- `apply` never deletes a whole device type, profile, integration, connector or dashboard.
- `export` writes drafts. A draft that was never published is exported all the same, and `apply`
  publishes it on the other side.
