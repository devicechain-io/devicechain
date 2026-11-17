---
sidebar_position: 6
title: 灾难恢复
---

# 灾难恢复 {#disaster-recovery}

恢复 DeviceChain 实例需要两样东西：数据库备份，以及实例的**密钥存储根密钥**。大多数备份流程会保存前者，却漏掉后者。本页介绍根密钥的作用、如何保存可用于恢复的副本，以及如何结合数据库备份和根密钥重建实例。

## 两套备份 {#two-tiers}

实例的数据分别存放在两台数据库服务器上，需要分别备份和恢复：

| | **核心数据**（PostgreSQL） | **事件数据**（TimescaleDB） |
|---|---|---|
| 内容 | 租户、身份、设备、设备配置、检测规则、仪表板、连接器、最近已知状态，以及**所有已存储的密钥和凭据** | 测量值、事件历史、汇总数据 |
| 大小 | 通常为兆字节级；随设备群的*配置*增长 | 数据量较大；随时间增长 |
| 丢失的后果 | 无法重建实例 | 历史数据丢失，但实例仍能运行 |
| 是否需要根密钥 | **需要** | 不需要 |

这种划分来自平台实际的数据存储方式，并非对同一个数据库施加的策略：

- `event-management` 负责事件存储，包括其 schema、超表和保留策略，也是唯一向其中写入遥测数据的服务。
- 其余服务都将自己的数据保存在关系数据库服务器上。
- 另一个会访问 TimescaleDB 的服务只有 `user-management`。它使用访客连接，不创建 schema，也不执行迁移，仅用于删除已清除租户的数据行。

没有任何写入跨越两个存储，因此不需要在它们之间维护事务一致性。

但如果两部分恢复到不同时间点，有一件事需要特别注意。关系数据库中的租户记录驱动上述删除操作，并在删除完成后被移除。如果将事件数据恢复到租户清除*之前*，而核心数据恢复到其记录移除*之后*，就会重新引入已经没有任何机制负责清除的遥测数据。

规划恢复时应考虑以下两点：

- **备份计划不同。** 两个存储都通过基础备份加持续归档的预写日志进行备份，但基础备份频率和保留期限不应相同。核心数据较少，只有配置发生变化时才会变化。事件数据量大，主要以追加方式写入，而且已经受[数据生命周期](../concepts/architecture.md)保留策略管理。为生命周期协调器即将删除的数据块继续保存基础备份，相当于为同一批数据支付两次存储费用。两个存储的恢复窗口也不同：默认情况下，核心数据可恢复到最近 30 天内的任意时间点，事件数据则为最近 7 天（参见[恢复窗口](./bootstrap.md#backup-retention)）。无论使用 `--restore-rdb-at` 还是 `--restore-tsdb-at`，目标时间都必须位于对应存储的窗口内；更早的数据已不在归档中。因此，若要将两个存储恢复到同一时间点，必须选择较短窗口内的时间。若核心数据恢复到更早的时间，而事件数据恢复到较晚的时间，就会出现反方向的不匹配：在核心数据目标时间之后创建的租户仍有遥测数据，却没有租户记录来驱动删除。
- **恢复目标不同。** 只恢复核心数据也能得到一个*可以工作*的实例：设备能够重新连接、检测规则能够运行、命令能够派发、密钥能够解密。恢复事件数据则为实例补回历史。缺少事件数据的实例处于降级状态（历史组件为空），而非停机，因此两部分可以采用不同的恢复时间目标。

根密钥仅影响核心数据。事件数据不包含密文，因此 TimescaleDB 恢复不需要本页介绍的密钥步骤。下文的密钥处理都针对核心数据。

## 为什么根密钥需要单独处理 {#root-key}

DeviceChain 保存的每项秘密都使用独立的数据密钥进行静态加密，包括为登录令牌签名的密钥、出站连接器凭据、SMTP 密码和 AI 提供商密钥。每个数据密钥又由实例级的**根密钥**封装，也称密钥加密密钥（KEK；参见[架构](../concepts/architecture.md)）。无论使用哪种配置，每个实例都会保存令牌签名密钥，因此每个实例都依赖根密钥：没有它，任何人都无法登录。

根密钥保存在实例的 Kubernetes Secret 中，也就是保存在 **etcd** 中。数据库备份不包含 etcd。PostgreSQL 备份只归档 PostgreSQL，TimescaleDB 备份只包含 TimescaleDB，两者都没有保存根密钥的任何字节。

这会导致一种常见恢复演练发现不了的故障：

- **在原集群中就地恢复数据库**时，一切正常，因为 etcd 中仍有密钥。这种演练会带来错误的信心。
- **恢复到新集群**时，也就是实际灾难场景中，加密数据行可以完整恢复，恢复操作也报告成功，但其中每项秘密都永久无法读取。新集群生成了*另一个*根密钥，而旧密钥无法从剩余资料中推导出来。

过去，这种问题往往要到很久以后才以无法解释的解密错误暴露，此时有用的备份可能早已轮换删除。现在有两项检查，分别在不同阶段依据不同信息发现问题：

- **创建任何资源之前**，`dcctl bootstrap` 会查询关系存储中该实例已有的内容。如果数据库存在，却并非由当前集群创建，说明它在原集群消失后保留了下来。此时 bootstrap 会停止，避免生成新密钥覆盖恢复条件。这项检查用于防止错误发生。
- **服务启动时**，保存秘密的服务会用根密钥尝试打开自己的存储记录；若无法解密，就拒绝提供服务。这项检查发现的是错误密钥，例如恢复时指定了错误的托管文件，而不是缺少密钥。仅查看存储内容无法识别这种情况。

启动检查会阻止整个 API 运行，而不只是集成功能。`user-management` 保存令牌签名密钥，因此也会拒绝启动。其他服务都必须拿到 `user-management` 的签名密钥后才会报告就绪，所以它们同样无法提供服务，任何人都无法登录。

两项检查都会让问题立即明确暴露，避免以后出现零散错误，但它们都不能恢复丢失的密钥。密钥一旦丢失，就无法找回。

:::danger 丢失根密钥后无法恢复
根密钥由 256 位随机数据组成，封装后的数据密钥无法通过暴力破解恢复。密钥丢失意味着秘密丢失，提交支持工单也无法找回。其中包括令牌签名密钥，因此根密钥丢失或错误的实例**无法启动**：`user-management` 会拒绝启动，其他服务也无法就绪。这是 DeviceChain 中无法补救的一项状态，所以默认启用下述密钥托管。
:::

## 密钥托管文件 {#the-escrow-artifact}

`dcctl bootstrap` 会写入一个**加密的密钥托管文件**：这是一个小型文本文件，包含使用你选择的口令加密封装的根密钥。

```
~/.devicechain/escrow/<instance>-rootkey.escrow
```

文件本身包含说明。即使某人在多年后第一次看到它，也能从文件中了解它是什么、保护哪些内容、丢失会有什么后果，以及恢复时应执行的完整命令，无须查阅本页。

两个属性尤其重要：

- **它与实例分开存储。** 文件不会放在 `~/.devicechain/instances/<instance>/` 中，因为 [`dcctl destroy`](#after-destroy) 会删除该目录。`dcctl` 会拒绝指向该目录内部的 `--escrow-file` 路径。
- **它包含明文密钥指纹。** 无须提供口令，就能判断托管文件是否仍对应当前密钥。参见[验证](#verify)。

### 选择口令 {#choosing-a-passphrase}

Bootstrap 按以下顺序使用第一个可用的口令来源：

| 来源 | 用途 |
|--------|-----------|
| `--escrow-passphrase-file <path>` | 配合秘密管理器进行自动化；会移除末尾的换行符。 |
| `DCCTL_ESCROW_PASSPHRASE` | CI 和脚本化安装。如果变量已设置但为空，会报告错误，不会退回其他来源。 |
| 交互提示 | 在终端中手动操作时使用。需要输入两次，避免直到恢复时才发现拼写错误。 |

如果这些来源都不可用，而且没有终端可供询问，bootstrap 会失败。这是有意设计的，否则就会悄悄创建一个秘密随集群消失而丢失的实例。

:::warning 文件和口令应分开保存，并且都放在集群之外
把两者放在同一处，一次入侵就可能使保护失效。把两者都放在集群中，一次灾难就可能让备份完全失效。
:::

### 选择不托管 {#opting-out}

对于可以直接丢弃的实例，例如 CI 运行、演示或本地实验，可以传入 `--no-escrow`。`--dev` 会隐含启用该选项。

```bash
dcctl bootstrap local scratch --dev            # no escrow, disposable by construction
dcctl bootstrap local scratch --yes --no-escrow
```

Bootstrap 汇总会以红色明确提示。不要将其用于任何需要保留秘密的实例。

## 恢复实例 {#recover}

恢复过程会**创建**一个新集群，并在其中创建新实例。流程没有“恢复到运行中的实例”这一步：服务已经创建自己的 schema，此时在其下方恢复数据库，需要删除服务正在使用的表，并可能与迁移发生竞争。请通过重建进行恢复。

:::note 使用卷快照基础备份的集群
使用 [`--backup-snapshot-class`](./bootstrap.md#snapshot-base-backups) 时，下述两个步骤仍从备份存储恢复，而不会读取快照：它们使用最新的每周基础备份，以及此后归档的日志。因此，每个步骤可能需要重放长达一周的日志，耗时比使用每日基础备份更长。快照属于被替换的旧集群，重建流程不会读取它们。旧集群消失后，快照仍保留在云提供商处，新集群也不会清理它们。快照包含数据库内容，包括租户删除操作已经移除的数据。验证恢复后的实例后，应从提供商的快照列表中删除它们（Google Kubernetes Engine 用户参见[拆除集群](https://github.com/devicechain-io/devicechain/blob/main/deploy/gke/README.md#tearing-it-down)）。
:::

两个数据库需要按顺序使用两个命令恢复，因为它们的归属不同：

- 关系数据库在每个集群中只安装一次，保存**所有**实例的数据，因此由 `dcctl install` 恢复。
- 事件存储属于单个实例，因此由 `dcctl bootstrap` 恢复该实例的事件存储。

开始前，请确定实例名称。恢复时必须使用原实例名称，参见[使用实例原名恢复](#recover-under-the-instances-own-name)。

**1. 准备集群时，恢复共享关系数据库。**

```bash
dcctl install local --restore-rdb-from dc-rdb
```

`--restore-rdb-from` 指定备份存储桶内的文件夹。对于从未恢复过的存储，该文件夹为 `dc-rdb`，因为归档使用集群自身的名称写入。

对于另一类灾难，例如错误迁移或误删已经成功破坏数据，而你需要恢复到操作之前的状态，应添加 `--restore-rdb-at`。其值为严格早于破坏发生时间的 RFC 3339 时间戳。

传入原安装使用的同一个 `--backup-credentials-file`，让新集群读取旧集群写入的归档。

该恢复标志仅在关系存储*创建时*生效。如果目标集群已有关系存储，就不会移动任何数据；`dcctl` 会在应用任何内容之前明确提示，避免执行不完整的恢复。请在尚无关系存储的集群中安装并恢复。

恢复后，`dcctl` 会自行选择新的归档写入位置，没有用于设置该位置的标志。如果恢复后的数据库继续向读取恢复数据的原路径归档，会触发自身安全检查并在启动时卡住。`dcctl` 会分配独立路径，并在后续运行中保留该路径。

:::warning 数据行会恢复，密钥不会
数据库备份没有根密钥。恢复存储中的每项秘密仍由写入它的实例密钥封装，而该密钥只存在于刚刚丢失的集群中。在步骤 2 中，必须使用 `--restore-root-key` 重建每个实例。
:::

**2. 使用原根密钥重建实例，并恢复事件数据。**

```bash
dcctl bootstrap local my-instance \
  --restore-root-key ~/backups/my-instance-rootkey.escrow \
  --restore-tsdb-from dc-tsdb-my-instance-1a2b3c4d
```

实例的密钥存储根密钥从托管文件中导入，而不会重新生成。实例继续使用原先加密秘密的密钥，因此能够解密步骤 1 恢复的数据行。系统会询问托管文件的口令，也可以通过 `--escrow-passphrase-file` / `DCCTL_ESCROW_PASSPHRASE` 提供。

消息代理的凭据不在托管文件中，也不需要在其中。重建的实例会生成新的凭据，包括设备认证回调的密钥和消息代理身份账户的登录凭据以及每个功能区自己的代理登录凭据，并在同一次运行中交给消息代理和各个服务。旧的登录凭据一个也不会沿用，重建的实例也没有共用的代理登录凭据：每个功能区都用为它生成的登录凭据登录，已销毁实例的凭据无法打开任何东西。设备不持有其中任何一项：它们使用自己的凭据登录，而这些凭据会随数据库一起恢复。

`dcctl bootstrap` 会拒绝其他做法；这就是[前文](#root-key)所述两项检查中的第一项。它在写入任何内容之前，会查询恢复后的存储中该实例名称已有的数据；没有 `--restore-root-key` 的运行会停止，并提示所需标志。

这种拒绝机制是最后一道防线。只有托管文件仍存在的实例才能恢复。使用 `--no-escrow` 创建的实例没有根密钥的第二份副本，任何方式都无法再打开这些数据行。

`--restore-tsdb-from` 为可选项，与根密钥恢复独立。事件存储有意保留自己的时间线：将遥测数据回退到昨天，并不意味着控制平面也应回退，而且步骤 3 不依赖事件恢复。它同样支持使用 `--restore-tsdb-at` 指定时间点。每个实例的事件存储都向独立路径归档，因此应从归档中读取路径，不要猜测。单独的 `dc-tsdb` 是类似关系存储的命名方式，归档中不会存在这个路径。

:::warning 在仍运行的集群中恢复实例
如果要在现有集群中恢复某个实例的事件存储，例如回到误删之前，请先使用 `dcctl destroy --keep-backups` 删除实例，然后使用 `--restore-tsdb-from`（以及 `--restore-tsdb-at`）重新 bootstrap，**不要使用 `--restore-root-key`**。

这样重建的实例具有空的控制平面。Destroy 也会删除共享关系存储中的实例数据库，因此租户、设备、用户和已保存的秘密都会被删除，且不会恢复；只有事件历史会回来。如果要同时恢复两部分，请按上述步骤恢复整个集群。

由于实例数据库已被删除，没有任何数据需要使用托管密钥解密，所以 bootstrap 会拒绝这里的 `--restore-root-key`，并为重建的实例生成新根密钥。请先移走原托管文件，因为 bootstrap 不会覆盖已有文件（参见 [`--restore-root-key`](./bootstrap.md#useful-flags)），并保留它：它仍是打开 destroy 之前关系数据库备份的唯一密钥（参见 [`dcctl destroy` 之后](#after-destroy)）。

如果实例备份存放在集群自身的对象存储中，不带 `--keep-backups` 的 destroy 会删除恢复所要读取的归档。
:::

恢复是少数允许对已有实例执行的操作之一，因为恢复过程中可能发生中断，需要重试。更严格的保护机制只允许在托管文件携带的密钥与实例当前运行的密钥相同时重试，从而保证安全。

**3. 使用 `dcctl secrets escrow verify` 确认根密钥与托管文件一致**（参见[验证密钥托管](#verify)）。

能够登录，是密钥正确的第一个信号：只有根密钥能够打开封装的令牌签名密钥，`user-management` 才会启动。读取一个依赖秘密的对象，例如出站连接器或通知渠道，是更有力的检查。步骤 1 恢复对象所在的存储后，即可进行检查。仅恢复出数据行并不足以证明恢复成功；实际解密出的值才是证据。

**4. 如果恢复了事件数据，检查后台机制，而不只是行数。** 恢复后的事件存储可能拥有每一行数据，却已悄悄失去时间序列数据库的后台功能。表仍存在，查询仍有结果，但后台任务停止了。这种存储可以继续正常响应查询，直到磁盘被写满。

在事件存储的主节点上打开 shell。使用 CloudNativePG Operator 时，节点内的 `psql` 无须密码。下述命令中的两个名称都来自实例 ID，但不是同一个字符串：命名空间为 `dci-` 加实例 ID，而数据库名称只包含实例 ID，没有前缀。

```bash
kubectl -n dci-<instance-id> exec -it dc-tsdb-1 -c postgres -- psql -U postgres -d <instance-id>
```

接下来检查两件事。首先，事件表是否仍为超表？

```sql
SELECT hypertable_schema, hypertable_name FROM timescaledb_information.hypertables;
```

事件表位于 `event-management` schema，应该全部出现在列表中。如果恢复后变成普通表，这项检查就能发现。Schema 导出中普通表与超表看起来相同，无法据此识别。（不必查找 `measurement_rollups`。它是连续聚合，底层超表属于内部表，因此未出现在此列表中是正常的。）

第二项更关键：任务调度器是否真的在*当前集群*上运行？执行以下查询，等待一两分钟，再执行一次：

```sql
SELECT job_id, proc_name, total_runs, last_run_status
  FROM timescaledb_information.job_stats ORDER BY job_id;
```

**`total_runs` 必须增加。** 这就是检查标准。`total_runs` 是计数器，观察到它增长，才能证明当前集群实际在执行任务。

:::danger 不要依据 `next_start` 或 scheduled 标志判断
即使恢复后的调度器从未启动，所有任务仍可能显示为已调度，带有看似合理的未来 `next_start`，并永远保持这种状态。它看起来健康，**是因为数据已恢复，而不是因为任务将来会执行。**
:::

`next_start` 很容易被当作检查依据，但在这里不能说明问题。保存它的表是普通表，物理恢复会带回*旧集群*的值。

### 确认每个数据库都已完成恢复 {#check-recovery-finished}

`dcctl install` 和 `dcctl bootstrap` 会等待每个数据库处于健康状态，且所有实例均已加入；如果 15 分钟内未达到要求，命令会以错误结束。大型归档的日志重放可能超过这个时间，因此恢复可能需要多次运行。无法访问归档的恢复会一直等待，而不会立即失败。数据库健康并不能证明数据行已恢复，因此在认定恢复成功之前，请检查数据库本身：

```bash
kubectl get clusters.postgresql.cnpg.io --all-namespaces
```

关系数据库（`dc-rdb`）位于 `dc-system` 中；事件存储（`dc-tsdb`）位于实例自己的命名空间中。

应看到 `Cluster in healthy state`。停留在 `Setting up primary` 的集群尚未完成恢复。最常见的原因是归档无法访问，或者 `--restore-rdb-from` / `--restore-tsdb-from` 指定了存储桶中不存在的路径。

### 使用实例原名恢复 {#recover-under-the-instances-own-name}

恢复必须使用实例原来的名称。关系数据库以实例命名，因此步骤 1 会以*旧名称*恢复数据库，所有者为该实例自己的数据库登录身份。

如果在*不同名称*下使用 `--restore-root-key` 执行 bootstrap，命令会在存储中找不到对应数据库。如果集群中也没有同名的半建成实例，它会在写入任何内容之前停止：

```text
--restore-root-key recovers the key that opens instance "<new-name>"'s stored secrets, and
there is nothing here for it to open: the relational store holds no database for "<new-name>" ...
```

它拒绝继续，是因为否则会创建一个由恢复密钥保护的*空实例*：外部看起来恢复成功，实际上没有任何需要恢复的数据。同次运行中稍早打印的黄色 `note: ... was escrowed for instance "<old-name>"` 只是警告，不是拒绝执行的原因。

要继续恢复，请使用原名称重新运行步骤 2：也就是 `dcctl secrets escrow show` 对该托管文件显示的 `Instance:`，同时也是恢复后数据库使用的名称。

:::warning 开始前确定名称
拒绝执行发生在关系数据库恢复*之后*，因此应在开始恢复前确定名称，而不要等到处理事故时才决定。
:::

托管文件会记录写入时的实例名称，该名称也受到完整性验证保护：编辑名称会使文件失效。实例重命名属于迁移，而非恢复；平台目前不提供此操作。

## 在需要之前验证密钥托管 {#verify}

不再匹配运行密钥的托管文件，在真正使用前看起来与有效文件没有区别。请在日常维护时就进行检查：

```bash
dcctl secrets escrow verify ~/backups/my-instance-rootkey.escrow --instance my-instance
```

此命令将托管文件中的指纹与实例**实际运行中**的密钥比较。它无须口令，若不匹配则以非零状态退出，因此可集成到 cron 作业或 CI 检查中。不匹配意味着实例没有可用的托管副本，最常见的原因是写入文件后实例又被重新 bootstrap。

如果只想查看文件说明而不解密：

```bash
dcctl secrets escrow show ~/backups/my-instance-rootkey.escrow
```

:::warning `verify` 无法证明什么
它只能证明文件指向正确的密钥，不能证明文件仍能*解密打开*；后者需要口令。应定期演练真正的恢复：指纹检查只是报警器，不能替代恢复演练。
:::

## Bootstrap 和升级期间的凭据 {#credentials-and-the-two-commands-that-touch-them}

有两个命令处理实例凭据：`dcctl bootstrap` 创建凭据，`dcctl upgrade` 保留凭据。

`dcctl bootstrap` 会**生成**实例的每项凭据，因为这些凭据尚不存在。这也是它拒绝对运行中的实例执行的原因：无论按什么顺序替换运行实例的凭据，都无法保证安全。

- 重写根密钥会使所有已保存的秘密永久无法读取。
- 重写消息代理凭据可以补救，但会造成中断。消息代理和服务由不同机制、按不同时间更新，新凭据会产生一段双方互相拒绝认证的窗口，在此期间启动的 Pod 可能完全无法连接消息代理。

`dcctl upgrade` 用于操作运行中的实例，**不会生成任何新凭据**。它读取并保留下列所有内容：

- 密钥存储根密钥；
- 消息代理的认证授权信息和登录凭据（callout issuer seed、每个功能区的登录凭据和系统密码）；
- 数据库密码（共享所有者、provisioner、实例自己的登录身份和事件存储所有者）；
- 服务间认证秘密；
- 启用监控和集群内备份时的 Grafana 管理员密码和对象存储凭据。

版本变更不能变成凭据变更。它唯一能创建的是此前不存在的凭据：在设备认证回调加密之前构建的实例，首次升级时会生成该回调的加密密钥，
与签发者种子一起保存在实例的回调 Secret 中，此后永不替换。消息代理身份账户（保存登录状态）的登录凭据也是如此：只生成一次，保存在仅由 `user-management` 挂载的 Secret 中，此后保持不变。每个服务各自的代理登录凭据也是如此：在各服务拥有自己的登录凭据之前构建的实例，首次升级时一次性生成这些凭据，此后保持不变。登录状态不会从备份中恢复，因此重建后所有人需要重新登录。

### 完成中途失败的 bootstrap {#resuming-a-bootstrap}

拒绝重复执行的判断依据有两个：接近运行末尾才写入的实例**配置文档**，以及实例声明中表示首次 bootstrap 尚未完成的记录。只有成功结束的运行才会移除该记录。没有配置文档或仍带有该记录的实例属于半建成状态，而非已上线状态；重新运行同一个 bootstrap 是完成创建的受支持方式。它会复用前次运行留在集群中的根密钥、消息代理凭据和数据库密码，报告还会显示超级用户的自动生成密码，而失败的前次运行尚未执行到这一步。首次 bootstrap 由旧版本启动的实例没有这条记录。参见 [bootstrap 执行的操作](./bootstrap.md#what-it-does)。

消息代理是必须保留这一重试窗口的原因。它的配置比实例本身提前几个步骤完成。如果 bootstrap 在两者之间失败，就会留下一个运行中的消息代理，而后续运行无法仅通过集群识别其凭据：消息代理只保存公钥和两个密码哈希，无法反推出服务所需的原始凭据。

因此，`dcctl` 在配置消息代理之前，还会将其凭据保存到执行命令的本地机器，位于 `~/.devicechain/instances/<instance>/` 下。如果尚无实例可供查询，后续运行就会从该文件复用凭据。文件仅当前用户可读，`dcctl destroy` 会将其与其他实例本地状态一并删除。

### 每次升级都会核对托管文件 {#escrow-reconcile}

检查托管文件无须口令。文件记录了所保护密钥的指纹，将它与实例当前运行的密钥比较不涉及解密文件。因此，`dcctl upgrade` 每次都会进行检查，无须用户提供任何内容：

| 托管文件状态 | 升级时的操作 |
|---|---|
| 与运行中的密钥匹配 | 确认匹配，并保留原文件。 |
| **不匹配** | 明确警告，并说明最可能的情况：这是同名旧实例的托管文件。用它恢复会得到一个无法读取自身秘密的集群。 |
| 缺失 | 如果传入了 `--escrow-passphrase-file`（或设置了 `DCCTL_ESCROW_PASSPHRASE`），就写入一份。这也是最初使用 `--no-escrow` 创建的实例后来获得托管副本的方式。如果没有可用口令，则发出警告，不会询问：升级应能无人值守执行，而且到这个阶段实例已经完成更新。 |

这些结果都不会导致升级失败。托管问题关系到未来的灾难恢复，而当前升级关系到正在运行的实例。无法升级的运维人员可能会绕过检查，而不是修复问题。

## 更换根密钥 {#replace-root-key}

当前版本不支持原地轮换根密钥。没有任何命令可以换入新密钥，并重新封装旧密钥所保护的内容。要更换根密钥，需要重新创建实例，而重新创建的实例从空的控制平面开始。

以下四类已保存的数据依赖根密钥：

- **实例保存的所有机密。** 每一项都由根密钥封装：出站连接器凭据、通知渠道的 SMTP 密码和 webhook 密钥、AI 提供方密钥，以及单点登录的客户端密钥。
- **令牌签名密钥。** `user-management` 同样用根密钥封装它，因此没有根密钥，任何人都无法登录。
- **设备 MQTT 密码摘要。** 计算设备 MQTT 密码摘要的密钥由根密钥派生。摘要是单向的，没有密码就无法在新密钥下重新计算。
- **设备访问令牌摘要。** 访问令牌只以摘要形式保存，计算摘要的密钥由根密钥派生。与密码摘要一样，没有令牌就无法在新密钥下重新计算，因此每台使用令牌的设备都需要重新设置令牌，或用同一令牌重新登记。

### 重新创建会丢失什么 {#replace-root-key-losses}

`dcctl destroy` 会删除实例在共享关系存储中的数据库，其中保存的一切都随之消失：租户、用户和角色、设备类型和配置文件、设备及其凭据、检测规则、仪表板、出站连接器、通知渠道、告警以及最新已知状态。这些都无法跨越密钥更换保留下来。关系数据库备份也无济于事：其中的机密由旧密钥封装，而恢复它会把旧密钥一并带回，这与更换密钥恰恰相反。没有任何命令可以整体导出实例的配置。仪表板可以从控制台导出并导入新实例，设备可以从 CSV 文件导入（[导入设备](../guides/importing-devices.md)）。其余内容都需要重新创建。

事件历史可以保留下来。事件存储中没有密文，因此其备份不需要根密钥。使用 `--keep-backups` 执行 destroy，再使用 `--restore-tsdb-from` 执行 bootstrap，方法见[恢复实例](#recover)。事件历史按租户令牌和设备令牌存储，因此会与你用相同令牌重新创建的租户和设备对应起来。对于没有重新创建的租户，其遥测数据会留在事件存储中，却没有租户行来驱动删除。如果不带 `--keep-backups`，destroy 会删除保存在集群自身对象存储中的事件存储备份。

### 重新创建实例 {#replace-root-key-steps}

如果怀疑根密钥可能已经泄露：

1. 导出能导出的内容：仪表板定义，以及一份需要重新创建的配置记录。
2. 把旧的托管文件移到别处。bootstrap 不会覆盖已有的托管文件。请保留它：它是打开 destroy 之前关系数据库备份的唯一密钥。
3. 执行 [`dcctl destroy <provider> <instance> --keep-backups`](./bootstrap.md#destroy)，然后执行 [`dcctl bootstrap <provider> <instance>`](./bootstrap.md)，并加上 `--restore-tsdb-from` 以恢复事件历史。不要传入 `--restore-root-key`：destroy 之后，bootstrap 会拒绝该参数。新实例会生成自己的根密钥，并写出新的托管文件。
4. 重新创建租户、用户、配置文件、规则、连接器和渠道，并重新登记设备；对使用 MQTT 密码的设备，为每台设备提供密码。如果提供相同的密码，设备可以继续使用旧密码，但持有旧根密钥和旧数据库副本的人可以离线针对其摘要尝试猜测。足够长的随机密码可以抵御这种猜测，短密码则不行，因此要为这类设备设置新密码。
5. 在签发方轮换旧实例保存过的凭据：SMTP 密码和 webhook 密钥、出站连接器使用的消息代理和云凭据、AI 提供方密钥，以及单点登录客户端密钥。持有旧根密钥和旧数据库副本的人可以读取这些凭据，因此要在新实例中填入新的凭据。固件签名密钥不在此列：实例只保存其公钥部分。

持有托管文件及其口令的人就持有该实例的根密钥，因此请将两者分开存放。

原地轮换计划在首个稳定版本之后提供。

## 数据库之外还存放了什么 {#other-state}

除两个数据库外，实例还在三处保存数据：消息流、键值存储桶和对象存储。恢复对每一项负有三种义务之一，下表给出了平台创建的每个消息流和存储桶的答案：

- **must-back-up。** 它保存着平台已经确认过的某些数据的唯一副本。数据库中没有它的记录，所以遗漏它的恢复会让它永久丢失。
- **rebuildable。** 平台可以从数据库重新计算或重新发布它，或者由它所属的设备或人员再次发送。恢复后它从空开始，这是正确的。
- **transient。** 它处于传输中或存活时间很短：会话、锁、管道缓冲区。恢复所丢失的内容受该项存活时长或管道延迟的限制。

只有三项是 must-back-up：原始采集流、无法送达的出站连接器投递的死信流，以及对象存储。其余一切都会自行恢复。

<!-- durability:start -->
| Item | Kind | Class | What a restore owes it |
|---|---|---|---|
| `inbound-events` | stream | transient | events accepted and not yet resolved; a pipeline buffer, so the most a restore loses is the lag |
| `integration-uplinks` | stream | transient | raw decoder input already accepted from an integration and not yet decoded; an in-flight buffer |
| `resolved-events` | stream | transient | events resolved and not yet stored; the event store is the landing, so only the in-flight lag is lost |
| `derived-events` | stream | transient | events derived by detection and not yet consumed; an in-flight buffer |
| `device-attribute` | stream | rebuildable | attribute changes are projected from the device store, which is restored from the database |
| `device-commands` | stream | rebuildable | commands are re-dispatched from the command-delivery store by its sweep |
| `device-desired` | stream | rebuildable | the current desired state is held in the database and sent to a device again on its next sync |
| `device-reports` | stream | rebuildable | a device sends its reports again, and its state is held in the database |
| `device-events-capture` | stream | must-back-up | raw device telemetry already acknowledged to the device and not yet forwarded; the database holds no copy of it. Freeze producers and consumers, then export with dcctl backup and verify with dcctl backup inspect --verify. Coordinate the database recovery cutoff separately; restore --plan is read-only and does not apply a restore. |
| `command-responses` | stream | transient | a device answer not yet consumed; the command it answers is settled from its stored state as unanswered |
| `connector-dispatch` | stream | transient | outbound deliveries not yet attempted; in flight, at least once |
| `lpwan-downlink-events` | stream | transient | a network-server report not yet matched to its command; a lost report is settled by the pending record's deadline as unanswered |
| `detection-rules-published` | stream | rebuildable | a rule publish is re-derived from the stored profile version |
| `device-roster` | stream | rebuildable | roster changes are projected from the device store |
| `geofence-set-manifest` | stream | rebuildable | a fence edit is re-derived from the stored fence set |
| `entity-deleted` | stream | transient | a deletion fan-out in flight; a deletion announced just before the restore point may not have reached every consumer |
| `device-ownership` | stream | rebuildable | ownership changes are projected from the stored relationships |
| `agent-actuation-kills` | stream | rebuildable | the policy head retains the kill state and operators can repeat a kill |
| `alarm-events` | stream | rebuildable | alarm state is held in the database and notification re-derives from it |
| `raise-alarm` | stream | transient | an in-flight request from detection, which evaluates again on the next event |
| `failed-decode` | stream | transient | a diagnostic record of an event that could not be decoded |
| `failed-events` | stream | transient | records not yet stored by their consumer; the event store keeps each one once it is consumed |
| `connector-dispatch.dead` | stream | must-back-up | the only record of an outbound delivery that could not be made, kept for the operator to inspect. Export with dcctl backup while producers and consumers are frozen, then verify with dcctl backup inspect --verify. Records absent from the backup cannot be recovered once the stream is lost; restore --plan does not apply a restore. |
| `dead-letters` | stream | transient | letters not yet stored by their consumer; the database keeps each one once it is consumed |
| `max-deliveries` | stream | transient | broker advisories for deliveries that ran out; empty in steady state |
| `dc_refresh_tokens` | bucket | transient | a live session token; people sign in again |
| `dc_oauth_codes` | bucket | transient | an authorization code that lives sixty seconds |
| `dc_sso_state` | bucket | transient | an in-flight single sign-on attempt that lives ten minutes |
| `dc_sso_codes` | bucket | transient | a completed single sign-on awaiting redemption for sixty seconds |
| `dc_mfa_challenges` | bucket | transient | a second-factor challenge that lives five minutes |
| `dc_credential_attempts` | bucket | transient | sign-in backoff counters that expire after ten minutes |
| `dc_device_credential_attempts` | bucket | transient | device connect backoff counters that expire after ten minutes |
| `dc_locks` | bucket | transient | held locks that expire after thirty seconds |
| `dc_sparkplug_commands_awaiting` | bucket | transient | commands in flight inside a confirm window of at most two hours |
| `dc_partition_placement` | bucket | rebuildable | a mirror of the placement table in the user-management database, rewritten every few minutes |
| `dc_leases` | bucket | transient | partition leases that expire after thirty seconds; a restore must also be treated as a fence invalidation, because the epoch is the bucket revision and a rebuilt bucket restarts it |
| `device-by-token` | bucket | rebuildable | a cache of the device store |
| `relationships-by-source` | bucket | rebuildable | a cache of the relationship store |
| `memberships-by-entity` | bucket | rebuildable | a cache of the group membership store |
| `profile-resolution-by-type` | bucket | rebuildable | a cache of the published profile versions |
| `scoped-groups-exist` | bucket | rebuildable | a cache of the group store |
| `$MQTT_msgs` | gateway stream | transient | a second copy of every QoS 1 and 2 message, held only until the platform stream has it |
| `$MQTT_out` | gateway stream | transient | outgoing QoS 2 release records, in flight |
| `$MQTT_qos2in` | gateway stream | transient | inbound QoS 2 handshake state, in flight |
| `$MQTT_sess` | gateway stream | rebuildable | persistent MQTT sessions and their subscriptions; a device that subscribes on every connect recovers by itself, one that relies on the broker having remembered its subscriptions must be made to subscribe again |
| `$MQTT_rmsgs` | gateway stream | rebuildable | retained messages; the platform publishes none itself, so each one is whatever a device or an integration retained and must publish again |
| `object-store` | object store | must-back-up | update artifact content and tenant branding images; the database holds only each object's size and digest. Snapshot the volume, or turn on versioning and replication for the bucket, and take that copy after the database base backup it belongs with. |
<!-- durability:end -->

此表由平台自身的声明生成，因此它列出的是运行中的服务所创建的内容。没有分类，就无法添加消息流或存储桶。

### 导出流和对象内容 {#export-streams-objects}

以下 `v1.6.0` 命令处于**预览**阶段。它们补充数据库备份和根密钥托管；不会导出任何数据库或根密钥，不会选择数据库恢复截止点，也不会协调维护模式。

导出前请冻结生产者、消费者、上传和删除操作。对于文件系统后端，请在本地以只读方式挂载或导出 blob PVC：`--blob-directory` 指向包含实例目录的根目录（本例中实例目录为 `devicechain/`）。命令不会创建 PVC 导出 Job。实例目录缺失或存在未完成的上传时，导出会失败。对于 S3，省略 `--blob-directory`：使用已部署的 bucket/endpoint 配置及运维人员的标准 AWS 凭据链，不会从服务 Pod 中获取凭据。

```bash
dcctl backup devicechain --destination ./backup-set --blob-directory /mnt/blob-export
dcctl backup inspect ./backup-set --verify
dcctl restore devicechain --from ./backup-set --plan --blob-directory /mnt/target-blob-export
```

`backup` 要求目标目录尚不存在，且实例已安装。`--kube-context <context>` 选择集群；`--nats-url <url>` 可替代默认的 broker 端口转发。备份和恢复规划都使用已部署的 broker 管理员凭据及 TLS；broker 需具备此版本的快照/分块确认权限。规划命令的 `--blob-directory` 读取目标 PVC 导出，而非源备份。

导出包括所有已声明的检查点保留平台流（即使为空）及其消费者、存在时的 `$MQTT_msgs`，以及实例前缀下所有已完成对象，包括未被引用的内容。缺少必需的平台流会导致导出失败；MQTT 网关流缺省时会显式记录。流文件包含原生快照及配置/状态元数据；对象 tar 包含内容及原始引用、大小、SHA-256 哈希索引（S3 返回时还包含内容类型/版本 ID）。文件检查完成后最后发布 `manifest.json`。导出失败可能留下无清单的诊断文件；请改用新目录重试。仅凭这些文件不能证明数据库/流/对象处于协调一致的恢复检查点。

`backup inspect --verify` 必须带 `--verify`，不访问集群，也不写入。它验证清单及每个已列出快照的大小和 SHA-256；出错时返回非零状态。成功仅证明所列字节与清单一致：不验证清单真实性，不检查原生归档内部，也不证明清单完整或检查点一致。

`restore --plan` 必须带 `--from` 和 `--plan`；**尚未实现执行恢复**。连接前先验证本地字节、实例/租户身份、必需清单覆盖范围及可用快照元数据，然后读取目标流状态和对象字节，输出 create/replace/skip 操作而不执行。缺少身份的旧清单可检查，但不能用于规划。缺失需保留的条目、未知条目或错误策略均被拒绝。仅当 `absent_gateway_streams` 中显式记录且目标也不存在时，才允许省略 `$MQTT_msgs`。

对于非空目标流，缺少最后消息时间、时间戳更新、序列号更高或消息更多都会导致拒绝。目标对象有额外内容或内容不同（包括后端不同）也会拒绝；匹配对象会跳过，相同内容的子集可补齐。目标存储缺失或不可读属于错误。`--force-foreign` 仅允许实例/租户身份不匹配：不会绕过覆盖范围或数据丢失检查，也不迁移 subject、租户、blob 引用或后端。规划期间请冻结目标写入；输出只是一次观察，不是锁，也不为后续执行预留状态。

### 对象存储 {#object-store-backup}

对象存储保存更新制品的内容和租户的品牌图片。数据库只保存每个对象的大小和摘要，所以单独恢复数据库会带回一个内容可能缺失的目录。

- **卷（默认）。** 使用集群的卷快照，或存储类提供的备份策略为该卷创建快照（chart 的 `blobStorage.persistence.annotations` 会把它带到卷声明上）。
- **兼容 S3 的存储桶。** 存储桶归您所有，因此平台不会复制它。请开启版本控制和复制，或使用云服务商的备份服务。
- **在与之配套的数据库基础备份之后再做副本。** 对象只写入一次，所以比数据库更新的副本会多出数据库不知道的几个对象，这不会造成危害。比数据库更旧的副本，会让数据库列为可用的制品背后没有内容。收到这种制品的设备无法下载它。请停用该制品，并用同一文件重新声明；凡使用过被停用制品的更新，都要重新分配。

### 分区租约与单写入者区域 {#restore-fence}

租约存储桶 `dc_leases` 保存每个单写入者所有者（检测、Sparkplug 和 LwM2M 接入区域、外部 MQTT 来源）对其分区的占有。它不会被备份，恢复后从空开始。其纪元计数器就是存储桶自身的修订号，所以也会重新开始。请在恢复数据库之前停止这些区域，并在恢复之后再启动它们，这样在授予新的占有时，旧占有的所有者不会仍在运行。

## `dcctl destroy` 之后 {#after-destroy}

`dcctl destroy` 会删除实例的 Helm release、NATS 消息代理、事件存储、数据库及数据库登录身份、命名空间和本地状态。它**不会**删除托管文件，因为文件特意保存在该目录之外。Destroy 结束时会提示托管文件的位置。

Destroy 不会删除集群，也不会删除 `dcctl install` 安装的前置资源：其他实例使用的关系数据库以及备份对象存储仍保留。对于存放在集群自身对象存储中的备份，destroy 会删除被销毁实例自己的事件存储备份。用户提供的外部对象存储中的备份从不会被删除，`--keep-backups` 也可以保留集群内备份；参见[实例备份的处理方式](./bootstrap.md#destroy-backups)。对于使用[卷快照基础备份](./bootstrap.md#snapshot-base-backups)的集群，无论是否保留备份，实例快照都会随其命名空间一起移除。

只要仍保留该实例任何数据库备份，就必须保留托管文件，它是仍能读取这些备份的唯一密钥。应在备份全部删除之后再删除托管文件，不能提前删除。
