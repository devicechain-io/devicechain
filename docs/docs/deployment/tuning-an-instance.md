---
sidebar_position: 10.5
title: Tuning an Instance
---

# Tuning an Instance {#tuning-an-instance}

You tune an instance — replica counts, CPU and memory, an area's own configuration — with a
values file. You give it to `dcctl bootstrap` or `dcctl upgrade` with `--values`, and `dcctl`
**records it on the instance**, so every later `dcctl upgrade` applies it again without being
asked. Without that record, an upgrade rebuilds the instance's settings from its declaration
and your tuning is reverted.

```yaml title="tuning.yaml"
functionalAreas:
  device-management:
    replicas: 3
    resources:
      limits:
        cpu: "3"
  event-sources:
    config:
      capture:
        publishWindow: 512
```

```bash
# At bootstrap
dcctl bootstrap local my-instance --values tuning.yaml

# On a running instance
dcctl upgrade local my-instance --values tuning.yaml

# Every later upgrade keeps applying it; nothing to repeat
dcctl upgrade local my-instance --version <new-version>
```

## How the overlay combines with what dcctl sets {#precedence}

Three layers, each over the one before:

1. The chart's defaults.
2. The values `dcctl` computes from the instance's declaration: images, credentials, endpoints,
   the profile and areas, and the sizing a preset such as `--compact` or `--ha` implies.
3. Your overlay.

Maps merge key by key, so you set only the keys you want to change. A list or a single value
replaces what is under it. Repeat `--values` to layer several files; later files win.

The overlay is applied to the same values the chart renders the instance configuration from, so a
setting such as `shutdownDrainSeconds` reaches the configuration document as well as the pods.

## What you can set {#tunable}

An overlay may set only the values below. Anything else is refused.

| Values | What they control |
| --- | --- |
| `replicas`, `rollingUpdate`, `terminationGracePeriodSeconds`, `nodeLossTolerationSeconds`, `shutdownDrainSeconds`, `podDisruptionBudget`, `startupProbe`, `livenessProbe` | Scale and rollout for every area |
| `resources`, `useMeasuredRequests`, `goMemLimitPercent`, `gogc`, `goMemLimit` | CPU and memory, and Go's memory behaviour, for every area |
| `functionalAreas.<area>.replicas`, `.strategy`, `.resources`, `.measuredRequests`, `.goMemLimitPercent`, `.memoryLimitFloor`, `.gogc`, `.goMemLimit`, `.avoidEventStorePrimary`, `.eventPathSpread` | The same, for one area |
| `functionalAreas.<area>.config` | That area's own configuration. An unknown key stops the service at startup |
| `frontend.replicas`, `frontend.resources` | The console's pods |
| `metrics.alerts`, `metrics.grafanaDashboards` | Alerting rules and dashboards |
| `ingress.className`, `ingress.annotations`, `ingress.uploadMaxBodySize`, `networkPolicy.additionalAllowedCidrs`, `serviceAccount.annotations` | How the ingress is fronted and who may reach the pods |

`functionalAreas.event-management.replicas` is not tunable even though the rest of that area is:
`--ha` sets it, and the event store's connection reserve is sized to it.

Some values inside a tunable subtree are refused too, because they are security switches or
wiring rather than capacity:

- In an area's `config`: `deviceAuthMode` (device-management), the `auth` block
  (user-management), `resourceUrl` and `issuerUrl` (mcp), `updates.allowUnverifiedArtifacts` and
  `updates.downloadBaseURL` (update-management), `eventSources` (event-sources), `sources`
  (sparkplug-ingest), any area's `tsdbConfiguration`, and `rdbConfiguration.sqlDebug`. Keys are
  matched without regard to case, the way the services read them.
- In `ingress.annotations`: every annotation the chart already sets on its ingresses (your value
  would replace the chart's), any `*-snippet` annotation, `cert-manager.io/*`, and `ssl-redirect`
  or `force-ssl-redirect`.

`serviceAccount.annotations` is accepted because it is where a cloud identity (workload identity,
or an IAM role for service accounts) is bound for the s3 object store. Every service pod of the
instance shares that service account, so all of them hold a role bound there: grant it no more
than the bucket needs.

Everything `dcctl` owns is refused: images and versions, credentials and the secret-store root
key, broker and database endpoints, the ingress host and TLS, the profile and the set of areas
(`dcctl enable-area` changes that), object-store settings, security contexts, and the LwM2M
settings that `dcctl` carries from one release to the next on its own. The
list is an allowlist on purpose: a value the chart gains in a later release stays refused until
it is added here, instead of becoming settable by accident.

A refusal names every offending key, and the file it came from, before anything is changed:

```text
--values tuning.yaml sets values dcctl does not let an overlay set, so nothing has been changed:
  image.tag: this is not an operator-tunable value: dcctl or the chart owns it (images, credentials, endpoints, exposure, the area set, security contexts)
```

The overlay is also checked against the chart's values schema, and rendered with the instance's
other values, before anything is applied. A value of the wrong type, a request above its limit, or
a combination the chart refuses stops the run with nothing changed.

## Sizing the event store {#event-store-sizing}

The event store is the one volume whose growth is set by traffic, so it is the one to size on
purpose. Three figures decide it.

- **Bytes per stored row.** A measurement row costs about 600 bytes on disk, indexes included,
  before compression: a 32Gi volume held about 51 million rows when it filled in a benchmark at
  10,000 readings per second. An earlier measurement put the figure near 1 KB with the
  write-ahead log; size with the larger figure until you have measured your own; the formula below takes the row size as a variable. The event store
  compresses chunks older than `lifecycle.compressAfterDays` (7 by default) in
  `event-management`'s config, losslessly, but it does nothing for the newest week: size the
  volume for at least that much uncompressed data. The 600-byte figure is the uncompressed one,
  and the compression ratio depends on your data, so measure it before counting on it.
- **Rows per reading.** Every [calculated metric](../user-guide/calculated-metrics.md) that
  applies to a profile stores one more row per reading. Each formula adds one row per reading, so a profile with N calculated metrics stores (1 + N) rows per reading, so N formulas multiply the volume by up to 1 + N. The tier ceiling `calculatedMetricsPerProfile` is 0 (off) by default and at most 32; it is a ceiling, and the cost depends on use.
- **Retention.** `lifecycle.retentionDays` is 0 by default, which keeps data forever. An
  instance meant to run indefinitely should set it, and then the volume only has to hold the
  window plus the uncompressed week.

As a rule, the uncompressed window needs readings per second × (1 + calculated metrics) × bytes per row × 86,400 seconds × days, with a quarter of the volume left free for the write-ahead log,
maintenance and growth. At the measured 600 bytes and 10,000 readings per second that is about 520 GB a day, so the default
seven uncompressed days is about 3.6 TB. Add what the compressed history and the retention window
need.

Three alerts watch the volume, in the order you want to hear them: `DatabaseVolumeFillsWithinDay`
(on course to fill within 24 hours), `DatabaseVolumeFillingFast` (within the hour) and
`DatabaseVolumeCritical` (95% full). If the database stops taking writes anyway,
`EventStoreRefusingWrites` fires and `event-management` holds the events it could not store
instead of dropping them (see [When the event store cannot take
writes](./observability.md#event-store-down)).

### Expanding the volume online {#event-store-expand}

On a StorageClass that allows volume expansion, raise the size on the event store's Cluster. The
database operator resizes each instance's claim in place, with no restart:

```bash
kubectl -n <instance namespace> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"storage":{"size":"48Gi"}}}'
kubectl -n <instance namespace> get pvc -w
```

If the database pod was already crash-looping on a full disk,
delete that pod once its claim has grown so it restarts on the larger filesystem. The held events
and the backlog behind them are then stored, and the alerts clear as the free fraction rises. Also set `timescale_storage` to the new size in the install's OpenTofu configuration. The Cluster is applied from that value: Helm's three-way merge keeps a hand-patched size only while the configured value is unchanged, a smaller configured value makes the database operator refuse the shrink and fails the upgrade, and a disaster-recovery rebuild comes back at the configured size. A StorageClass without expansion cannot grow a
volume: the only ways out are to shorten retention (which frees space as chunks are dropped) or to
restore into a larger cluster.

## Replicas and the database's connection budget {#connections}

Each instance's database login is limited to the connections its services can open: a full pool
for every pod, doubled to cover a rollout. An overlay that raises the replicas of an area that
uses the relational database, or that area's pool
(`functionalAreas.<area>.config.rdbConfiguration.maxOpenConnections`), raises that limit, and
`dcctl` asks the shared database for the larger limit before it changes anything. If the cluster's
connection budget cannot give it, the run stops with nothing changed: lower the overlay, or raise
the budget by re-running the `dcctl install` the cluster was installed with, adding `--max-connections` with a larger value (for example `dcctl install local --max-connections 1000`, plus `--kube-context`, `--ha` or `--compact` if it was installed with them). Removing the overlay gives the connections back.

## Changing or removing the overlay {#changing}

`--values` on `dcctl upgrade` **replaces** the recorded overlay as a whole; it does not merge into
it. To add a key, put it in the file beside the ones already there. To remove the overlay and
return the instance to the values `dcctl` computes:

```bash
dcctl upgrade local my-instance --reset-values
```

`--values` and `--reset-values` cannot be combined. `dcctl enable-area` and an upgrade given
neither flag keep the recorded overlay as it is.

## See what would change first {#dry-run}

`--dry-run` prints the difference between the recorded overlay and the one the run would leave:

```text
  Overlay: changes to the recorded tuning:
    ~ functionalAreas.device-management.replicas: 3 -> 4
    + gogc: 400
    - functionalAreas.device-state.replicas: 2
[dry-run] would record the values overlay in the instance declaration
```

`+` is added, `-` removed and `~` changed.

## Where the overlay is kept {#where}

The overlay is stored on the instance's declaration, in the cluster, as the
`core.devicechain.io/values-overlay` annotation. Any machine that can reach the cluster upgrades
with it; nothing is kept in a file on the machine that bootstrapped. Destroying the instance
removes it.

Anyone who can read the instance declaration can read the overlay. It cannot set credentials, but
an area's `config` is free-form, so keep secrets out of it.

## When an upgrade refuses the recorded overlay {#stale}

A new release can rename a value or tighten what it accepts. An upgrade checks the recorded overlay
against the release it is moving to, before changing anything, and stops with the key named if it
no longer fits. Fix it with `--values <fixed-file>`, or remove it with `--reset-values`.

If the recorded overlay cannot be read at all, an upgrade given neither flag stops rather than
running without it; `--values` or `--reset-values` replaces it.

## What applying it does {#rollout}

An overlay is applied through the same release as every other setting, so a change that alters a
pod's settings or an area's `config` rolls that area's pods; running pods do not re-read it. Tuning that moves load between services, such
as replica counts on the event path, is worth rehearsing with `--dry-run` and rolling out when the
instance is quiet.
