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
| `perf-12k` | One cluster without `--ha`, about 48 vCPU, sustained 12,000 events a second with event envelopes on. | Measured: 12,000 events a second held clean on GKE, with every event accounted for. |

A size for a higher rate is added only after a measurement shows that rate clean. The name is the
claim, so a size is never published as a target.

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

Each row is one pod. With `--ha` the counts below are multiplied by what HA decides, and the
sizes of each copy stay as written.

<!-- size-table:perf-12k:areas -->
| Service | Replicas | CPU limit | Memory limit | CPU request |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `500m` |
| `device-state` | `2` | `3` | `512Mi` | `250m` |
| `event-management` | - | `4` | `1Gi` | `400m` |
| `event-processing` | - | `2` | `1Gi` | `500m` |
| `event-sources` | `3` | `3` | `512Mi` | `600m` |
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
<!-- /size-table:perf-12k:infra -->

The JetStream volume is sized so that the reservation of the design rate fits in it; a volume
that an instance already has is never resized by an upgrade, so a size applied to an existing
instance keeps the volumes it was built with.

### What `perf-12k` does not set {#perf-12k-not}

- **Event envelopes.** The figures were measured with them on, and they cut CPU by about a fifth.
  They are a separate switch, so `dcctl bootstrap` tells you when a `perf` size runs with them
  off.
- **`--ha`.** The rate was measured on one broker server. The combination is allowed, and
  `dcctl bootstrap` tells you it has not been measured.
- **The shared database and the nodes.** The relational store's volume and `max_wal_size`, the
  database CPU and memory requests, the monitoring volume and the node pools belong to the cluster
  and stay as `dcctl install` and your cluster left them. The measured cluster used three node
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
