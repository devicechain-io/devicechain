---
sidebar_position: 10.6
title: 选择规格
---

# 选择规格 {#choosing-a-size}

实例的规格是一个带名字的选择。`dcctl` 每次都把这个名字展开成同一组设置，因此在一个集群上测得的数据可以在另一个集群上复现，而不必再靠一串覆盖条目和 `TF_VAR_` 导出去重建。

规格是**预设，而不是旋钮**。它组合已有的设置，从不改变运行哪些服务（那仍由[配置档](./bootstrap.md)决定），也从不开启某种行为：事件信封和 MQTT 故障关闭确认保持你设定的状态。它与 `--ha` 相互独立：`--ha` 决定运行几份副本，规格决定每份副本多大。

| 规格 | 适用场景 | 状态 |
| --- | --- | --- |
| `base` | 笔记本或小型集群。默认值：未指定规格的实例就是 `base`，渲染结果与规格出现之前完全相同。 | 标准规格，见[服务规格](./bootstrap.md#service-sizing)。 |
| `compact` | 资源很少的笔记本。这是集群级设置，在安装时选择；`--compact` 与之等价且会保留。 | 见 [`--compact`](./bootstrap.md#--compact)。 |
| `perf-12k` | 一个不带 `--ha` 的集群，约 48 个 vCPU，在开启事件信封的情况下持续每秒 12,000 个事件。 | 已实测：在 GKE 上稳定处理每秒 12,000 个事件，所有事件均有对账。 |

只有在测量显示某个更高速率稳定之后，才会添加对应的规格。名字就是承诺，因此规格绝不会以“目标”的形式发布。

## 如何选择 {#choosing}

```bash
dcctl install <provider> --kube-context <context>
dcctl bootstrap <provider> <instance> --size perf-12k
```

`dcctl install` 上的 `--size compact` 等同于 `--compact`。`perf` 规格按实例选择，在 `dcctl bootstrap` 上指定。紧凑集群会拒绝 `perf` 规格。

规格会记录在实例上，`dcctl upgrade` 每次都会重新应用它，因此升级绝不会改变实例的规格。某个版本如果调整了某个规格，用该规格记录的实例会在下一次升级时随之改变，该版本的升级说明会写明这一点。

## `perf-12k` 设置了什么 {#perf-12k}

每一行对应一个 Pod。使用 `--ha` 时，下面的数量会乘以 HA 决定的倍数，每份副本的大小保持不变。

<!-- size-table:perf-12k:areas -->
| 服务 | 副本数 | CPU 上限 | 内存上限 | CPU 请求 |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `500m` |
| `device-state` | `2` | `3` | `512Mi` | `250m` |
| `event-management` | - | `4` | `1Gi` | `400m` |
| `event-processing` | - | `2` | `1Gi` | `500m` |
| `event-sources` | `3` | `3` | `512Mi` | `600m` |
<!-- /size-table:perf-12k:areas -->

短横线表示该规格未作设定，沿用 Chart 自身的值。`device-management` 保留其内存下限而不设自己的上限，因为由规格写入的上限会绕过该下限。除非 `--ha` 另有决定，`event-management` 保持一个副本。

Broker、卷以及事件存储预写日志的额度：

<!-- size-table:perf-12k:infra -->
| 设置 | 值 |
| --- | --- |
| `streamDesignRate` | `12000` |
| `nats_cpu_request` | `4` |
| `nats_memory_request` | `4Gi` |
| `nats_memory_limit` | `8Gi` |
| `nats_jetstream_storage` | `28Gi` |
| `timescale_storage` | `160Gi` |
| `timescale_max_wal_size_mib` | `20480` |
<!-- /size-table:perf-12k:infra -->

JetStream 卷的大小按设计速率的预留量能装下来确定；升级绝不会调整实例已有的卷，因此把规格应用到现有实例时，会保留它创建时的卷。

### `perf-12k` 不设置什么 {#perf-12k-not}

- **事件信封。** 这些数据是在开启信封时测得的，信封约可降低五分之一的 CPU。它是单独的开关，所以当 `perf` 规格在关闭信封的情况下运行时，`dcctl bootstrap` 会提示你。
- **`--ha`。** 该速率是在单个 Broker 服务器上测得的。这种组合是允许的，`dcctl bootstrap` 会提示它尚未实测。
- **共享数据库与节点。** 关系型存储的卷和 `max_wal_size`、数据库的 CPU 与内存请求、监控卷以及节点池都属于集群，保持 `dcctl install` 和你的集群留下的状态。实测的集群使用三个节点池：服务用 `t2d-standard-8`（两个节点），数据库用 `n2d-standard-8`（两个节点），Broker 单独用一个 `t2d-standard-8`，压测发生器在别处。

## 哪个值优先 {#precedence}

从低到高：

1. Chart 的默认值和 OpenTofu 变量的默认值；
2. 规格；
3. `dcctl` 自己算出的内容：HA 的副本数、信封开关、运行的服务、实例已有的卷；
4. 你的 `--values` 覆盖文件；
5. 你在 `TF_VAR_*`、`terraform.tfvars` 或 `*.auto.tfvars` 文件中声明的 OpenTofu 变量。

你声明的内容优先。当你已自行声明某个 OpenTofu 变量时，`dcctl` 根本不会传递规格的同名变量，否则 OpenTofu 会采用规格的值而非你的值。`dcctl bootstrap` 打印的那一行会写明所应用的规格。

## 更改运行中实例的规格 {#changing}

实例保留其 bootstrap 时的规格。把现有实例换到另一个规格不属于本版本；在此之前，请把想要的设置写进覆盖文件，它优先于任何规格。无论哪种规格，`dcctl` 都无法扩容 JetStream 卷，操作步骤见[事件流能等待多久才扛过一次中断](./observability.md#outage-runway)。
