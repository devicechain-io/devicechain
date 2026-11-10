---
sidebar_position: 10.5
title: Tuning an Instance
---

# Tuning an Instance {#tuning-an-instance}

:::note Sizing figures assume fail-closed off
The throughput figures in [Measured throughput](./bootstrap.md#measured-throughput) were taken with the [fail-closed MQTT acknowledgement](./observability.md#mqtt-fail-closed) off, the default. Without it, sustained MQTT overload past the capture stream's margin drops the oldest acknowledged events (an [accepted gap](../concepts/security-model.md#accepted-gaps)). If that matters to you, turn it on with `--mqtt-fail-closed` and size below its lower ceiling.
:::

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
| `functionalAreas.<area>.profiler.enabled`, `.blockRate`, `.mutexFraction` | The diagnostic profiling listener, off by default (see [Observability](./observability.md)). Its `address` is not tunable |
| `frontend.replicas`, `frontend.resources` | The console's pods |
| `metrics.alerts`, `metrics.grafanaDashboards` | Alerting rules and dashboards |
| `ingress.className`, `ingress.annotations`, `ingress.uploadMaxBodySize`, `ingress.gateway.className`, `ingress.gateway.annotations`, `networkPolicy.additionalAllowedCidrs`, `serviceAccount.annotations` | How the ingress is fronted and who may reach the pods |
| `mqttExposure.loadBalancer` | The cloud settings of the MQTT load balancer, such as an internal balancer and allowed source ranges |
| `instance.config.infrastructure.nats.streamDesignRate`, `.streamRunwayMinutes`, `.streamMaxBytes`, `.streamMaxBytesCold`, `.streamMaxMsgs` | The outage runway the event streams are sized for, and their byte and message ceilings. The reservation is checked against the broker's volume before anything is written |
| `instance.config.infrastructure.messaging.envelopes.resolved`, `.inbound`, `.inboundLingerMillis` | Whether events are written as envelopes, off by default |
| `instance.config.infrastructure.tokenAudience.enforcement`, `.unscopedClientCredentials`, `.externalResources` | Token audiences are always enforced. Leave enforcement unset or set it to `enforce`; `v1.4` refuses `observe`. The separate `unscopedClientCredentials: observe` setting remains supported. `externalResources` lists resource servers outside the platform. The chart derives and owns the other two keys. |
| `brokerAdmin.allowCeilingShrink` | Lets one upgrade lower a stream ceiling below what the stream holds. Set it for that upgrade and remove it after |

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
  Counting every table an event is written to (events and measurements, indexes included), a
  later GKE measurement read 756 bytes per stored event, uncompressed.
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

### Checkpoints, chunk size and compression at higher rates {#event-store-checkpoints}

Three settings decide how the event store behaves once the rate climbs past a few thousand
readings per second.

- **Checkpoints.** The first change to each page after a checkpoint writes the whole 8 KB page to
  the write-ahead log, so checkpoints that arrive too often multiply the log. The event store
  logs compressed page images (`wal_compression = lz4`) and sets `checkpoint_timeout` to 15
  minutes and `max_wal_size` to a fifth of the volume (6.4 GiB on the default 32Gi), so a store
  taking a few thousand readings per second checkpoints on the clock instead of whenever the log
  fills. Both are applied by reload, with no restart. The cost is crash recovery: after an unclean
  stop the database replays the log written since its last checkpoint, up to about 30 minutes of it, since a checkpoint is spread over most of its interval.
  Raise `timescale_max_wal_size_mib` and lower `timescale_checkpoint_timeout` together when you
  size the store for a higher rate: to keep checkpoints on the clock, `max_wal_size` wants to be
  at least three times the log written per checkpoint interval. The log lives on the data volume,
  so that figure has to fit inside it.
  **Sizing rule.** Postgres requests a checkpoint, ahead of the timer, once the log written since
  the last one reaches about `max_wal_size` / (1 + `checkpoint_completion_target`), roughly
  `max_wal_size` divided by 1.9. To keep the event store's checkpoints on the clock, set
  `max_wal_size` to at least 1.9 × `checkpoint_timeout` (900 seconds by default) × the log rate in
  bytes per second. Measure the rate on your own store (`wal_bytes` in `pg_stat_wal`, over a
  window); it depends on row width and indexes. Worked example from a GKE benchmark: with the
  default 13 GiB limit the event store checkpointed on the clock at 8,000 MQTT events a second
  (9.4 MB/s of log), but at 12,000 a second (15.3 MB/s) it requested a checkpoint instead of
  waiting for the timer, because 15 MB/s writes about 7 GB in under eight minutes. MB/s here
  means 10^6 bytes a second. At 15.3 MB/s the rule gives 1.9 × 900 × 15.3 MB/s = 26.2 GB, which is
  24.4 GiB, so set about 25 GiB at 12,000 a second. Scaling the measured rate to 20,000 a second
  (about 25.5 MB/s) gives about 40.6 GiB, so about 41 GiB there; that second figure is an
  extrapolation, not a measurement. Round up in both cases and measure your own rate. The log lives
  on the data volume, so size the volume for that limit as well as for the data. The factor of three above is the margin to
  keep on top of this minimum.
  These settings tune the event store only. The relational database keeps its own defaults
  (`max_wal_size` of 1 GB and a five-minute `checkpoint_timeout`); in the same benchmark it requested
  3 to 6 checkpoints in every 7.5-minute window at 4.5 to 7 MB/s of log, which is expected there and
  is not a sign that the event store is mistuned.
  An upgrade applies this to an existing store too, at its current volume size. If that volume
  is already close to full, run `dcctl upgrade --event-store-max-wal-mib 1024` to keep the previous 1 GiB (OpenTofu users set `timescale_max_wal_size_mib` to 1024).
- **Chunk interval.** `lifecycle.chunkIntervalHours` is 24 by default and only affects chunks
  created after it changes. A chunk's indexes should fit in memory while it is being written, and
  the active chunks' indexes should fit in about a quarter of the database's RAM. At 1,000
  readings per second a daily chunk is tens of gigabytes and the default is right. At
  10,000 and above, use 1 to 6 hours.
- **Compress-after.** `lifecycle.compressAfterDays` is 7 by default, which keeps a week of data
  uncompressed. At 10,000 readings per second that is several terabytes of hot data. If you
  do not read raw data older than a day or two, set it to 1 and size the volume for that.

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
and the backlog behind them are then stored, and the alerts clear as the free fraction rises. Also set `timescale_storage` to the new size in the install's OpenTofu configuration. The Cluster is applied from that value: Helm's three-way merge keeps a hand-patched size only while the configured value is unchanged, a smaller configured value makes the database operator refuse the shrink and fails a direct apply (`dcctl upgrade` itself keeps the volume's live size and never shrinks it), and a disaster-recovery rebuild comes back at the configured size. A StorageClass without expansion cannot grow a
volume: the only ways out are to shorten retention (which frees space as chunks are dropped) or to
restore into a larger cluster.

### When the resize does not reach the volumes {#event-store-expand-pvc}

The database operator applies a new `spec.storage.size` to the claims only while it reconciles the
Cluster, and an unhealthy Cluster may not. In the full-volume incident behind these notes both
standbys were crash-looping on `No space left on device`, and patching the Cluster did not change
the claims within about three minutes; patching the claims directly did. If `kubectl get pvc` still
shows the old capacity a few minutes after you patched the Cluster, grow the claims yourself. The
same applies to the relational database (Cluster `dc-rdb`, claims `dc-rdb-N`), which
`DatabaseVolumeFillingUp`, `DatabaseVolumeAlmostFull` and `DatabaseVolumeCritical` watch as well as
the event store.

1. Check that the StorageClass allows expansion. Without it the patch is refused, and the only ways
   out are shortening retention (which frees space as chunks are dropped) or restoring into a larger
   cluster:

   ```bash
   kubectl -n <instance namespace> get pvc dc-tsdb-1 -o jsonpath='{.spec.storageClassName}{"\n"}'
   kubectl get storageclass <that class> -o jsonpath='{.allowVolumeExpansion}{"\n"}'   # must print true
   ```

2. Patch every claim of the Cluster to the same size (`dc-tsdb-1`, `dc-tsdb-2`, `dc-tsdb-3`). A claim
   can only grow:

   ```bash
   for pvc in dc-tsdb-1 dc-tsdb-2 dc-tsdb-3; do
     kubectl -n <instance namespace> patch pvc "$pvc" --type merge \
       -p '{"spec":{"resources":{"requests":{"storage":"48Gi"}}}}'
   done
   ```

3. Keep the Cluster's `spec.storage.size` and the `timescale_storage` setting at the same size, as
   described above, so the operator and the next apply agree with what you did.

Verify before you call it done:

- `kubectl -n <instance namespace> get pvc` shows the new capacity under `CAPACITY` for every
  member. A claim left with a `FileSystemResizePending` condition grows its filesystem when its pod
  restarts: delete the pod, one member at a time, waiting for it to be ready again before the next,
  and the primary last.
- `df -h` inside the database container (`kubectl -n <instance namespace> exec dc-tsdb-1 -c postgres
  -- df -h /var/lib/postgresql/data`) reports the larger filesystem.
- The alerts clear as the free fraction rises: `DatabaseVolumeCritical` and
  `DatabaseVolumeAlmostFull` first, then `DatabaseVolumeFillingFast` and
  `DatabaseVolumeFillsWithinDay`. `CNPGClusterNotReady` and `EventStoreRefusingWrites` clear once
  the standbys have restarted and `event-management` has stored what it held;
  `JetStreamDurableFallingBehind` clears when the backlog on `resolved-events` has drained. In the
  incident the backlog drained in about 70 seconds once the volume had grown, and every event
  was stored exactly once.

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
