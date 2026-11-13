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
| `perf-12k` | 一个不带 `--ha` 的集群，平台节点池约 40 个 vCPU，在开启事件信封的情况下持续每秒 12,000 个事件。 | 已实测：在 GKE 上稳定处理每秒 12,000 个事件（确认延迟 p99 低于 4 毫秒），所有事件均有对账。少数取值根据该次运行修正，尚未按此取值实际运行；见下文。 |
| `perf-16k` | 同一形态，用于持续每秒 16,000 个事件：更大的 Broker、预写日志额度和 event-processing Pod。每秒 20,000 个事件是仅就吞吐而言的上限，不是目标。 | 由同一次 GKE 运行推导：该次运行精确处理了每秒 16,000 个事件；这里的规格以 50 毫秒确认延迟为目标，而该次运行仅略微未达标（`perf-12k` 规格下为 63 毫秒）。尚未按此取值实际运行；见下文。 |

规格以它在延迟目标之内能够稳定处理的速率命名。名字就是承诺，因此规格绝不会以“目标”的形式发布。这就是没有 `perf-20k` 的原因：实测的形态精确存储了每秒 20,000 个事件，没有丢失也没有重复，但确认延迟的第 99 百分位为 245 毫秒，所以 20,000 只作为 `perf-16k` 的吞吐上限记录在文档中，而不是一个规格。

## GKE 上的 broker 存储

当 GKE 的 SSD StorageClass 可用时，`dcctl bootstrap` 创建的新 GKE broker 使用 `premium-rwo`（`pd-ssd`）。JetStream 的同步磁盘写入使磁盘延迟尤为重要，使用 `--ha` 的三个副本时更是如此；SSD 可避免依赖小容量 balanced 磁盘的最低 IOPS。其他云和 kind 继续使用集群默认存储类。OpenTofu 实例输入 `nats_jetstream_storage_class`（模块输入 `jetstream_storage_class`）默认为 `""`，此时省略 `config.jetstream.fileStore.pvc.storageClassName`。如需其他存储类，请在首次 apply 前设置。

现有 PVC 的存储类及 StatefulSet 的卷声明模板不可变。升级时，以及 bootstrap 发现现有 broker 或 JetStream 卷声明时，`dcctl` 不设置此输入。模块通过 `lifecycle.ignore_changes` 保留首次选择（包括未设置的情况），后续默认值不会改变它。现有 GKE broker 保留当前磁盘；本次变更不会将数据迁移到 SSD。

## 如何选择 {#choosing}

```bash
dcctl install <provider> --kube-context <context>
dcctl bootstrap <provider> <instance> --size perf-12k
```

`dcctl install` 上的 `--size compact` 等同于 `--compact`。`perf` 规格按实例选择，在 `dcctl bootstrap` 上指定。紧凑集群会拒绝 `perf` 规格。

规格会记录在实例上，`dcctl upgrade` 每次都会重新应用它，因此升级绝不会改变实例的规格。某个版本如果调整了某个规格，用该规格记录的实例会在下一次升级时随之改变，该版本的升级说明会写明这一点。

## `perf-12k` 设置了什么 {#perf-12k}

每一行对应一个 Pod。`--ha` 决定 `event-management` 的副本数（两个）以及 Broker 服务器和数据库实例的数量（各三个）；下面其他服务的副本数保持不变，每份副本的大小也保持不变。

<!-- size-table:perf-12k:areas -->
| 服务 | 副本数 | CPU 上限 | 内存上限 | CPU 请求 |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `400m` |
| `device-state` | `2` | `3` | `512Mi` | `250m` |
| `event-management` | - | `4` | `1Gi` | `400m` |
| `event-processing` | - | `2` | `1Gi` | `500m` |
| `event-sources` | `3` | `3` | `256Mi` | `600m` |
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
| `postgres_max_wal_size_mib` | `4096` |
<!-- /size-table:perf-12k:infra -->

JetStream 卷的大小按设计速率的预留量能装下来确定；升级绝不会调整实例已有的卷，因此把规格应用到现有实例时，会保留它创建时的卷。

最后一行属于集群的关系型存储，而不是实例。bootstrap 不会传递它：请使用 `dcctl install --relational-store-max-wal-mib 4096` 设置，并同时声明一个留有余量的卷，例如 `TF_VAR_postgres_storage=16Gi`。选择在应用前记录，之后安装时省略标志会保留该选择。已记录的值会显式传递，并覆盖 `TF_VAR_postgres_max_wal_size_mib`；`--relational-store-max-wal-mib=0` 会记录恢复 PostgreSQL 自带的 1GB 的选择。它通过集群定义到达数据库（数据库 Operator 不允许 `ALTER SYSTEM`）。此更改只需重新加载，不会重启任何 Pod。由于日志与数据共用一个卷，如果取值超过 `postgres_storage` 的四分之一，计划会被拒绝。使用默认的 1GB 时，在这个速率下，每一两分钟就会因日志量而触发一次检查点。

### 实测与推导 {#perf-12k-derived}

配置形态（上面的副本数和 CPU 上限，以及每秒 12,000 个事件）是在 GKE 上以 40 个 vCPU 的平台节点池实测的；该集群 48 个 vCPU 中的其余部分是压测发生器。之后在同一形态上的一次运行稳定处理了每秒 12,000 个事件，所有事件均有对账，确认延迟 p99 为 3.7 毫秒，没有重启，也没有因日志量而请求检查点。同一次运行还给出了三个**尚未按此取值实际运行**的取值：`device-management` 的 400m 请求、`event-sources` 的 256Mi 内存上限（常驻内存最多 81 MiB），以及关系型存储的 4096 MiB 额度。若后续测试调整了它们，升级说明会写明。

### 事件库卷能撑多久 {#event-store-volume}

在事件库中，一个已存储的事件约占 756 字节，包含索引、未压缩（事件表 345 字节，测量值表 454 字节）。在饱和的每秒 12,000 个事件下，这相当于每小时 32.7 GB，因此 `160Gi` 的卷在扣除日志额度和工作余量之后，大约能容纳三个半小时的持续写入。`perf-16k` 设置 `260Gi`，在每秒 16,000 个事件（每小时 43.6 GB）下大约能撑四个小时。这些是突发和基准测试用的卷，不是留存用的卷。早于 `lifecycle.compressAfterDays`（默认 7）的数据块会被压缩，因此持续使用时请设置数据生命周期并据此确定卷的大小：见[事件库的容量规划](./tuning-an-instance.md#event-store-sizing)。当卷即将写满时，`DatabaseVolumeFillsWithinDay` 告警会触发。

## `perf-16k` 设置了什么 {#perf-16k}

`perf-16k` 是 `perf-12k` 的形态，并调高了从每秒 12,000 到 20,000 的爬坡测试中显示为瓶颈的参数。各服务的副本数和 CPU 上限不变；请求则随每个 Pod 的实测用量上调。

<!-- size-table:perf-16k:areas -->
| 服务 | 副本数 | CPU 上限 | 内存上限 | CPU 请求 |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `500m` |
| `device-state` | `2` | `3` | `512Mi` | `300m` |
| `event-management` | - | `4` | `1Gi` | `450m` |
| `event-processing` | - | `2` | `1536Mi` | `700m` |
| `event-sources` | `3` | `3` | `256Mi` | `750m` |
<!-- /size-table:perf-16k:areas -->

`event-processing` 设为 2 CPU 和 1536Mi：1 个 CPU 在每秒 16,000 个事件起就被限流，而 1.5Gi 配 2 CPU 是唯一在 20,000 时检测延迟仍低于水位线的配置。

Broker、卷以及预写日志的额度：

<!-- size-table:perf-16k:infra -->
| 设置 | 值 |
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

流的设计速率为 16000，JetStream 卷为 36Gi：按该速率应对 15 分钟中断所需的预留量（约 17.5 GiB，开启故障关闭确认时为 30 GiB）必须能装得下。Broker 请求 6 个 CPU，因为它在每秒 16,000 个事件时用了 4.25 个核，在 20,000 时用了 5.2 个核，超过 `perf-12k` 请求的 4 个。它的内存上限为 10Gi（工作集在 16,000 时为 3.6 GiB，在 20,000 时为 4.4 GiB）。事件库的额度按在每秒 20,000 个事件下检查点仍由时钟触发来确定。与上面一样，最后一行属于集群的关系型存储：请在安装时声明，并在旁边声明 `TF_VAR_postgres_storage=32Gi`。

### 实测与推导 {#perf-16k-derived}

实测运行使用的是 `perf-12k` 的规格，精确处理了每秒 16,000 个事件，确认延迟 p99 为 63 毫秒（目标为 50 毫秒）。此表中的每个取值都是从该次运行读出的，**尚未按此取值实际运行**；下一轮 GKE 测试会加以确认，若有变动，升级说明会写明。

### 每秒 20,000 个事件 {#perf-16k-ceiling}

在 `perf-12k` 的形态上，每秒 20,000 个事件被精确存储（1,320 万个事件，没有缺失也没有重复），没有丢失任何连接，确认延迟 p99 为 245 毫秒。这是吞吐上限：确认延迟超出目标，限制因素是单个 Broker 服务器（使用 5.2 个核，峰值 5.5）。`perf-16k` 的规格可以承载它，但这不是对延迟的承诺。

### `perf` 规格不设置什么 {#perf-12k-not}

- **事件信封。** 这些数据是在开启信封时测得的，信封约可降低五分之一的 CPU。它是单独的开关，所以当 `perf` 规格在关闭信封的情况下运行时，`dcctl bootstrap` 会提示你。
- **`--ha`。** 该速率是在单个 Broker 服务器上测得的。这种组合是允许的，`dcctl bootstrap` 会提示它尚未实测。
- **`--ha` 下的节点适配。** 这是 `dcctl bootstrap` 的检查；upgrade 不会运行此检查。使用 `--ha` 时，如果可调度节点中能各自满足 Broker 请求（`perf-16k` 为 6 CPU 与 6Gi，`perf-12k` 为 4 CPU 与 4Gi）的可分配 CPU 和内存的不足三个，`perf` 规格会被拒绝。检查在声明实例之前运行，因此可以用更小的 `--size` 重新运行。每个节点单独判断，一个节点的 CPU 与另一个节点的内存不会相加。错误信息会给出所见最大的节点以及解决办法：声明环境变量 `TF_VAR_nats_cpu_request` / `TF_VAR_nats_memory_request`，或在实例根目录的 `terraform.tfvars` 中设置 `nats_cpu_request` / `nats_memory_request`；该文件只有在实例根目录已存在时才会被读取，也就是重新运行时。首次 bootstrap 请使用环境变量。自行声明的请求不会被 `dcctl` 检查。其他选项是改用更小的 `--size` 或换更大的节点。它只比较可分配容量，不涉及空闲量或调度器的决定。`--dry-run` 下同样会拒绝，但集群无法连接时只会给出警告。
- **共享数据库与节点。** 关系型存储的卷、数据库的 CPU 与内存请求、监控卷以及节点池都属于集群，保持 `dcctl install` 和你的集群留下的状态。关系型存储的 `max_wal_size` 是同一类设置：规格只公布测量时所用的值，由你在安装时声明（见上文）。实测的集群使用三个节点池：服务用 `t2d-standard-8`（两个节点），数据库用 `n2d-standard-8`（两个节点），Broker 单独用一个 `t2d-standard-8`，压测发生器在别处。

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
