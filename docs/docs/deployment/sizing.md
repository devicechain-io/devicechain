---
sidebar_position: 10.6
title: Choosing a size
---

# Choosing a size {#choosing-a-size}

An instance's size is one choice with a name. `dcctl` turns the name into the same set of
settings every time, so a number measured on one cluster can be reproduced on another instead of
being rebuilt from a list of overlay entries and `TF_VAR_` exports.

A size is a **preset, not a dial**. It composes settings that already exist, it never changes
which services run (that stays on the [profile](./bootstrap.md)), and it never turns a behaviour
on: event envelopes and the fail-closed MQTT acknowledgement stay as you set them. It is separate
from `--ha`, which decides how many copies run, while a size decides how big each copy is.

| Size | For | Status |
| --- | --- | --- |
| `base` | A laptop or a small cluster. The default: an instance that names no size is `base`, and renders exactly what it rendered before sizes existed. | The standard sizing, described in [Service sizing](./bootstrap.md#service-sizing). |
| `compact` | A laptop with little to spare. The cluster's setting, chosen at install; `--compact` is the same thing and stays. | Described under [`--compact`](./bootstrap.md#--compact). |
| `perf-12k` | One cluster without `--ha`, platform pools of about 40 vCPU, sustained 12,000 events a second with event envelopes on. | Measured: 12,000 events a second held clean on GKE (acknowledgement p99 under 4 ms), with every event accounted for. A few values are corrected from that run and not yet run as written; see below. |
| `perf-16k` | The same shape for sustained 16,000 events a second: a larger broker, write-ahead log allowance and event-processing pod. 20,000 events a second is a throughput-only ceiling, not the target. | Derived from the same GKE run, which held 16,000 events a second exactly; the sizing here targets the 50 ms acknowledgement latency that run narrowly missed (63 ms on the `perf-12k` sizing). Not yet run as written; see below. |
| `perf-20k` | Throughput sizing for 20,000 events a second on one unsharded cluster without `--ha`, with event envelopes on. | Derived from the GKE ramp; not yet run as written. Acknowledgement latency is unvalidated. |

`perf-20k` is a throughput sizing target. The measured 20,000 events a second were stored exactly, with no loss or duplicates, but acknowledgement p99 was 245 ms, above the 50 ms target. This preset increases broker CPU on the same unsharded shape; it does not promise that latency target.

## Choosing one {#choosing}

```bash
dcctl install <provider> --kube-context <context>
dcctl bootstrap <provider> <instance> --size perf-12k
```

`--size compact` on `dcctl install` is the same as `--compact`. The `perf` sizes are chosen per
instance, on `dcctl bootstrap`. A `perf` size is refused on a compact cluster.

The size is recorded on the instance and `dcctl upgrade` applies it again every time, so an
upgrade never changes the size of an instance. A release that retunes a size changes the
instances recorded with it on their next upgrade, and its upgrade notes say so.

## What `perf-12k` sets {#perf-12k}

Each row is one pod. `--ha` decides the replica count of `event-management` (two) and the number of broker servers and database instances (three each); the replica counts below for the other services stay as written, and the size of every copy stays as written.

<!-- size-table:perf-12k:areas -->
| Service | Replicas | CPU limit | Memory limit | CPU request |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `400m` |
| `device-state` | `2` | `3` | `512Mi` | `250m` |
| `event-management` | - | `4` | `1Gi` | `400m` |
| `event-processing` | - | `2` | `1Gi` | `500m` |
| `event-sources` | `3` | `3` | `256Mi` | `600m` |
<!-- /size-table:perf-12k:areas -->

A dash means the size states nothing and the chart's own value applies. `device-management` keeps
its memory floor rather than a limit of its own, because a limit written by a size would skip the
floor. `event-management` keeps one replica unless `--ha` decides otherwise.

The broker, the volumes and the event store's write-ahead log allowance:

<!-- size-table:perf-12k:infra -->
| Setting | Value |
| --- | --- |
| `streamDesignRate` | `12000` |
| `nats_cpu_request` | `4` |
| `nats_memory_request` | `4Gi` |
| `nats_memory_limit` | `8Gi` |
| `nats_jetstream_storage` | `28Gi` |
| `timescale_storage` | `160Gi` |
| `timescale_max_wal_size_mib` | `20480` |
| `postgres_max_wal_size_mib` | `4096` |
<!-- /size-table:perf-12k:infra -->

The JetStream volume is sized so that the reservation of the design rate fits in it; a volume
that an instance already has is never resized by an upgrade, so a size applied to an existing
instance keeps the volumes it was built with.

The last row is the cluster's relational store, not the instance's. A bootstrap does not pass it: set it with `dcctl install --relational-store-max-wal-mib 4096`, together with a volume that leaves it room, for example `TF_VAR_postgres_storage=16Gi`. The choice is recorded before apply and retained on later installs when the flag is omitted. A recorded value is passed explicitly and overrides `TF_VAR_postgres_max_wal_size_mib`; `--relational-store-max-wal-mib=0` records a reset to PostgreSQL's own 1GB. It reaches the database through the cluster definition (the database operator does not allow `ALTER SYSTEM`). The change is reload-only and restarts no pods. The plan refuses a value above a quarter of `postgres_storage`, because the log shares the data volume. At the stock 1GB, this rate triggers checkpoints by log volume every minute or two.

### Measured and derived {#perf-12k-derived}

The shape (the replicas and CPU limits above, and the 12,000 events a second) was measured on GKE with 40 vCPU of platform pools; the rest of the 48 vCPU of that cluster was the load generator. A later run on the same shape held 12,000 events a second with every event accounted for, an acknowledgement p99 of 3.7 ms, no restarts and no checkpoint requested by log volume. The same run read three values off it that have **not yet been run as written**: `device-management`'s 400m request, `event-sources`' 256Mi memory limit (81 MiB resident at most), and the relational store's 4096 MiB allowance. The upgrade notes say if a later round moves them.

### How long the event store volume lasts {#event-store-volume}

A stored event costs about 756 bytes in the event store, indexes included and before compression (345 bytes in the events table and 454 in the measurements table). At a saturated 12,000 events a second that is 32.7 GB an hour, so the `160Gi` volume holds about three and a half hours of continuous ingest after the log allowance and working room. `perf-16k` sets `260Gi`, which holds about four hours at 16,000 events a second (43.6 GB an hour). These are burst and benchmark volumes, not retention. Chunks older than `lifecycle.compressAfterDays` (7 by default) are compressed, so for sustained use set the data lifecycle and size the volume for it: see [Sizing the event store](./tuning-an-instance.md#event-store-sizing). The `DatabaseVolumeFillsWithinDay` alert fires when the volume is on course to fill.

## What `perf-16k` sets {#perf-16k}

`perf-16k` is the `perf-12k` shape with the knobs that the 12,000 to 20,000 ramp showed limiting it raised. The replica counts and CPU limits of the services are unchanged; the requests rise with the measured use per pod.

<!-- size-table:perf-16k:areas -->
| Service | Replicas | CPU limit | Memory limit | CPU request |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `500m` |
| `device-state` | `2` | `3` | `512Mi` | `300m` |
| `event-management` | - | `4` | `1Gi` | `450m` |
| `event-processing` | - | `2` | `1536Mi` | `700m` |
| `event-sources` | `3` | `3` | `256Mi` | `750m` |
<!-- /size-table:perf-16k:areas -->

`event-processing` gets 2 CPU and 1536Mi: one CPU was throttled from 16,000 events a second, and 1.5Gi with 2 CPU was the only configuration whose detection lag stayed under the watermark at 20,000.

The broker, the volumes and the write-ahead log allowances:

<!-- size-table:perf-16k:infra -->
| Setting | Value |
| --- | --- |
| `streamDesignRate` | `16000` |
| `nats_cpu_request` | `6` |
| `nats_memory_request` | `6Gi` |
| `nats_memory_limit` | `10Gi` |
| `nats_jetstream_storage` | `36Gi` |
| `timescale_storage` | `260Gi` |
| `timescale_max_wal_size_mib` | `40960` |
| `postgres_max_wal_size_mib` | `8192` |
<!-- /size-table:perf-16k:infra -->

The stream design rate is 16000 and the JetStream volume 36Gi: the reservation for a 15-minute outage at that rate (about 17.5 GiB, and 30 GiB with the fail-closed acknowledgement on) has to fit in it. The broker requests 6 CPU because it used 4.25 cores at 16,000 events a second and 5.2 at 20,000, which is more than the 4 that `perf-12k` requests. Its memory limit is 10Gi (working set 3.6 GiB at 16,000 and 4.4 GiB at 20,000). The event store allowance is sized so that checkpoints stay on the clock at 20,000 events a second. As above, the last row is the cluster's relational store: state it at install, with `TF_VAR_postgres_storage=32Gi` beside it.

### Measured and derived {#perf-16k-derived}

The measured run used the `perf-12k` sizing and held 16,000 events a second exactly, with an acknowledgement p99 of 63 ms (50 ms is the target). Every value in this table is read off that run and has **not yet been run as written**; the next GKE round confirms it, and the upgrade notes say if it moves.

### 20,000 events a second {#perf-16k-ceiling}

On the `perf-12k` shape, 20,000 events a second were stored exactly (13.2 million events, none missing, none duplicated) with no connection lost, at an acknowledgement p99 of 245 ms. It is a throughput ceiling: the acknowledgement latency is over its target and the single broker server is the limit (5.2 cores in use, peak 5.5). The `perf-16k` sizing carries it, but it is not a promise about latency.

## What `perf-20k` sets {#perf-20k}

`perf-20k` is a throughput sizing target. The measured 20,000 events a second were stored exactly, with no loss or duplicates, but acknowledgement p99 was 245 ms, above the 50 ms target. This preset increases broker CPU on the same unsharded shape; it does not promise that latency target.

It keeps the service replicas and CPU limits of `perf-12k`. Requests follow the same rule: measured CPU per pod at the design rate, multiplied by 1.2–1.5 and rounded up to 50m. Event-processing gets 1536Mi because that arm kept detection lag clean at 20k.

<!-- size-table:perf-20k:areas -->
| Service | Replicas | CPU limit | Memory limit | CPU request |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `600m` |
| `device-state` | `2` | `3` | `512Mi` | `400m` |
| `event-management` | - | `4` | `1Gi` | `650m` |
| `event-processing` | - | `2` | `1536Mi` | `950m` |
| `event-sources` | `3` | `3` | `256Mi` | `900m` |
<!-- /size-table:perf-20k:areas -->

<!-- size-table:perf-20k:infra -->
| Setting | Value |
| --- | --- |
| `streamDesignRate` | `20000` |
| `nats_cpu_request` | `6` |
| `nats_memory_request` | `6Gi` |
| `nats_memory_limit` | `10Gi` |
| `nats_jetstream_storage` | `40Gi` |
| `timescale_storage` | `260Gi` |
| `timescale_max_wal_size_mib` | `40960` |
| `postgres_max_wal_size_mib` | `8192` |
<!-- /size-table:perf-20k:infra -->

The single broker used 5.2 cores (peak 5.5) and 4.4Gi of working memory at 20k: reserve 6 CPU and 6Gi, with a 10Gi memory limit. The 40Gi JetStream volume holds the 15-minute runway reservation even with fail-closed acknowledgements on. The 260Gi event store and 40960 MiB WAL allowance follow the measured run; this is burst storage, not retention. The relational 8192 MiB WAL value is set separately at install with `dcctl install --relational-store-max-wal-mib 8192` and `TF_VAR_postgres_storage=32Gi`. These values have not yet been run as written.

### What the `perf` sizes do not set {#perf-12k-not}

- **Event envelopes.** The figures were measured with them on, and they cut CPU by about a fifth.
  They are a separate switch, so `dcctl bootstrap` tells you when a `perf` size runs with them
  off.
- **`--ha`.** The rate was measured on one broker server. The combination is allowed, and
  `dcctl bootstrap` tells you it has not been measured.
- **Node fit with `--ha`.** This is a `dcctl bootstrap` check; upgrade does not run it. With `--ha`, a `perf` size is refused when fewer than three schedulable nodes each have the broker's request (6 CPU and 6Gi for `perf-16k` and `perf-20k`, 4 CPU and 4Gi for `perf-12k`) available as allocatable CPU and memory. The check runs before the instance is declared, so a re-run with a smaller `--size` works. Each node is judged on its own; the CPU of one node and the memory of another do not add up. The message names the largest node it saw and the fixes: state the environment variables `TF_VAR_nats_cpu_request` / `TF_VAR_nats_memory_request`, or set `nats_cpu_request` / `nats_memory_request` in the instance root's `terraform.tfvars`, which is read only once the instance root exists, on a re-run. On a first bootstrap, use the environment variables. Requests you state yourself are left unchecked by `dcctl`. Other options are a smaller `--size` or larger nodes. It compares allocatable capacity only, not what is free or what the scheduler would decide. It also refuses on `--dry-run`, but a cluster that cannot be reached only produces a warning there.
- **The shared database and the nodes.** The relational store's volume, the database CPU and
  memory requests, the monitoring volume and the node pools belong to the cluster and stay as
  `dcctl install` and your cluster left them. The relational store's `max_wal_size` is the same
  kind of setting: the size publishes the value it was measured with, and you state it at install
  (see above). The measured cluster used three node
  pools of `t2d-standard-8` for services (two nodes), `n2d-standard-8` for databases (two nodes),
  and a `t2d-standard-8` for the broker alone, with the load generator elsewhere.

## Which value wins {#precedence}

From lowest to highest:

1. the chart's defaults and the OpenTofu variables' defaults;
2. the size;
3. what `dcctl` works out itself: the HA replica counts, the envelope switch, the services that
   run, the volumes an instance already has;
4. your `--values` overlay;
5. an OpenTofu variable you state in `TF_VAR_*`, `terraform.tfvars` or an `*.auto.tfvars` file.

Anything you state wins. `dcctl` does not pass a size's OpenTofu variable at all when you have
stated it yourself, because OpenTofu would otherwise take the size's value over yours. The line
`dcctl bootstrap` prints names the size it applied.

## Changing the size of a running instance {#changing}

An instance keeps the size it was bootstrapped with. Moving an existing instance to another size
is not part of this release; until then, state the settings you want in an overlay, which wins
over any size. Whatever the size, a JetStream volume cannot be grown by `dcctl`: see
[how long the event streams can wait out an outage](./observability.md#outage-runway) for the procedure.
