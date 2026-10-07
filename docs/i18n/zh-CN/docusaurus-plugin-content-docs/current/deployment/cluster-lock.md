---
sidebar_position: 2
title: 集群锁
---

# 集群锁 {#cluster-lock}

每次通过 `dcctl` 修改集群时，命令都会在应用任何变更之前获取该集群的**锁**，并一直持有到运行结束。这样，两位运维人员在不同机器上同时对同一个集群应用变更时，至少有一方会收到提示。

如果刚刚运行的命令告诉你集群已被占用，请阅读本页。

## 集群已被占用时的消息 {#claimed}

这类消息有两种形式。首先要看清你收到的是哪一种。

```
this cluster (working on instance "prod") is being worked on by
alice@build-01/48213/9f3c1a20b7e4d5c6, which renewed 4s ago; wait for it to finish
rather than running a second apply against the same cluster
```

有人正在对这个集群运行 `dcctl`。请等待对方完成。

```
this cluster (working on instance "prod") is claimed by
alice@build-01/48213/9f3c1a20b7e4d5c6, which last renewed 6m12s ago — by this
machine's clock that looks stale, but a clock that disagrees looks the same. If that
process is genuinely gone, take the claim with `dcctl instances reclaim
--kube-context prod-cluster`, which checks properly before it steals
```

锁已经有一段时间没有续约了。这是**证据，不是结论**。时间戳由另一台机器写入，由你的机器读取，两台机器的时钟差可能比你看到的间隔还大。判断持有者是否确实已退出，应交给 [`dcctl instances reclaim`](#reclaim)；它的检查不依赖两台机器的时钟一致。

持有者标识包含用户名、机器名、进程 ID 和随机后缀：`alice@build-01/48213/…`。你可以根据前三项核实进程。后缀用于防止同一用户在同一主机上的第二次运行被误认为第一次运行。

### 哪些命令拒绝执行，哪些只发出警告 {#refuse-or-warn}

| 命令 | 集群被其他人占用时的行为 |
|---|---|
| `dcctl install` | 拒绝执行，不会修改Operator或集群前置组件。 |
| `dcctl bootstrap` | 在第二步拒绝执行，不会修改基础设施或 Chart。 |
| `dcctl destroy` | 发出警告后继续：*“但如果该运行仍在执行，这次操作会与它冲突”*。 |
| `dcctl upgrade` | 发出同样的警告后继续。 |
| `dcctl bootstrap --dry-run` | 不获取锁，只报告实际执行时*会*遇到的占用情况。 |

初始化在第二步获取锁，而不是第一步。第一步是 `--build` 开发流程中的镜像构建，它不需要集群锁，只生成 Chart 随后要部署的镜像。使用已发布镜像时，第一步不做任何操作。

`dcctl upgrade` 遇到集群锁只会警告，但另有一项与锁无关的拒绝条件：如果集群中没有Operator，或可以确认Operator来自另一个发行版本，它不会将实例升级到目标版本，并会提示先运行 `dcctl install`。手动安装的Operator会收到提示，但可以继续。参见[发行与升级](./releases-and-upgrades.md#zero-downtime-upgrades)。

升级还会使用执行机器上的 OpenTofu 状态来应用实例的消息代理和事件存储配置。因此，如果另一台机器无视警告，同时升级同一个实例，它会同时应用自己那份状态。对同一个实例，每次只应从一台机器执行升级。

这种差异是有意设计的：

- **安装和初始化拒绝执行。** 两次初始化并行运行，可能拼出一个部分由第一份配置、部分由第二份配置构建的实例；拒绝第二次运行才有意义。
- **销毁和升级只发出警告。** 运维人员已经决定要拆除实例，往往正是因为出现了故障。无法先正常询问集群，不应成为阻止拆除的原因；所以这两个命令会明确警告，然后继续。
- **预演不获取锁。** 预演是计划，不应修改集群，因此什么也不写，包括锁。但它仍会报告锁的持有者，因为“已有其他运行”也是“这次操作将会做什么”的一部分。

## 锁的范围 {#scope}

**每个集群只有一把锁，而不是每个实例一把。** 一个集群可以运行多个实例，但初始化也会修改它们共享的组件：在共享关系数据库中创建实例的登录角色和数据库。`dcctl install` 修改的则*全部*是共享组件。DeviceChain Operator及其定义、入口控制器、cert-manager、CloudNativePG Operator等，由 [`dcctl install`](./bootstrap.md#install) 在集群中安装一次，而不是由每次初始化重复安装。这也是该命令获取同一把锁的原因。

即使两次运行操作的是*不同*实例，它们也都会应用共享部分，所以锁会将操作串行化。锁中记录实例 ID，以便拒绝消息告诉你持有者在操作哪个实例，但锁本身并不按实例 ID 划分。

:::note 这保证“每次只有一个运行”
锁阻止两个 `dcctl` 进程同时应用变更，但实例隔离不靠它实现：无论是否有人持锁，每个实例都有自己的命名空间和数据库登录角色。参见[同一集群中的多个实例](./bootstrap.md#what-it-does)。
:::

这把锁是一个名为 `dcctl` 的 Kubernetes `Lease`，位于 DeviceChain Operator所在的命名空间 `dc-k8s-system`。最先连接到集群的命令会创建该命名空间：`dcctl install` 将Operator安装在其中；初始化则确保它存在，让锁始终有地方存放。可以直接读取锁：

```bash
kubectl --context <kube-context> get lease dcctl -n dc-k8s-system -o yaml
```

锁在最后一次续约后 **60 秒**内有效，持有者每 **10 秒**续约一次。这个余量足以避免把一次慢 API 调用误认为进程死亡。正常完成、失败或被中断的运行会在退出时删除锁，而不是等待它过期，所以通常不会耽误下一位运维人员。

### 权限 {#rbac}

`dcctl` 使用执行者的身份操作。在由其他人管理的集群上，你的账户需要：

- 在 `dc-k8s-system` 中对 `leases.coordination.k8s.io` 的 `get`、`create`、`update` 和 `delete` 权限；
- 在整个集群范围内对 `instances.core.devicechain.io` 的 `get`、`list`、`create`、`update`、`patch` 和 `delete` 权限；
- 在 `dc-system` 中对 `secrets` 的 `list` 权限。

上述两项中的 `list` 都不可省略，而且最容易被漏掉。每次初始化和升级都会查询集群已有的实例及其占用资源，包括入口主机名、本地 MQTT 端口和连接预算。这需要列举，而不是读取单个对象。只有 `get` 权限的账户会在保护其他实例的检查中失败。`update` 和 `delete` 用于在销毁实例时释放声明的 finalizer，`patch` 用于记录声明上的运行阶段。

如果账户缺少这些权限，`dcctl` 会显示 API 服务器的实际拒绝信息：操作、资源、命名空间及用户，而不会把它报告成服务中断。

## 回收已退出运行留下的锁 {#reclaim}

运行若在归还锁之前被终止，例如笔记本丢失、SSH 会话断开或进程被 OOM 杀死，就会留下锁，直到有人回收。

```bash
dcctl instances reclaim --kube-context <kube-context>
```

这里的 `--kube-context` 并不是便利选项。这条命令是为使用*另一台*机器的运维人员准备的；那台机器没有初始化实例时留下的本地记录，只有明确指定集群，才能找到锁。

命令会显示锁的持有者、对方操作的实例，以及距上次续约的时间。然后要求你**完整输入持有者标识**：

```
  held by:   alice@build-01/48213/9f3c1a20b7e4d5c6
  instance:  prod
  renewed:   6m12s ago

Type the holder identity above to take the lock, or anything else to abort:
>
```

任何不完全匹配的输入都会取消操作，不改变锁。这条命令真正的风险是从仍在运行的进程手中夺锁。简单的“是/否”确认只增加形式，并不增加信息：不论有没有读上一行，都可以给出同一个答案。要求输入标识，会让你先看清这是谁的锁。

只有完成这一步后，命令才会开始检查：

```
checking whether the holder is still renewing (this takes about 1m0s)...
```

它检查的是：**在本机计时的一段完整窗口内，锁完全没有被修改。** 命令读取对象，用*你的*时钟等待一个完整的租约期限，再读取一次。活跃持有者会在这个窗口内续约六次；如果对象完全没有变化，就意味着没有续约到达 API 服务器。

这个过程不会比较两台机器的时钟。任何这类比较都受时钟偏差影响；最危险的方向是把活跃持有者判为已退出，而漂移的笔记本时钟就可能造成这种误判。

如果持有者在检查窗口中恢复并续约，或者恰好在检查完成与夺锁之间续约，回收操作会被**拒绝**，而不是悄悄覆盖活跃的占用记录。

成功后，命令会立即归还锁，使集群恢复可用：

```
the cluster lock is now free
```

回收命令不会替你继续持有锁。之后照常运行 `bootstrap`、`destroy` 或 `upgrade`。

:::danger 无法区分进程已死亡还是暂停
挂起的虚拟机、合上盖子的笔记本，以及被 `SIGSTOP` 暂停的进程，在这里看起来都与崩溃相同，等待再久也无法改变这一点。**夺锁之前，请询问对方或检查机器，确认另一个进程确实已退出。** 这条命令只能证明没有发生续约，不能证明以后不会续约。
:::

因此有意不提供 `--yes` 标志。无人值守回收需要作出“我知道那个进程已经退出”的判断，而标志无法承载这样的判断。

如果锁已经空闲，命令只会提示，不做任何操作：

```
this cluster is not claimed; there is nothing to reclaim
```

### 被回收的运行会发生什么 {#fenced}

被夺锁的运行会发现这一点，在开始下一步之前停止。持有者每十秒重新读取锁，检查它是否仍属于自己；`dcctl` 也会在每个步骤边界重新检查：

```
stopping before "Apply infrastructure": this cluster was reclaimed by another
operator: it is now held by bob@laptop/9912/3a7f…
```

失去锁的运行不会再向集群写入任何内容，包括自身实例声明上的阶段注解，因为该声明现在属于回收者。如果这样的运行原本已成功完成实例的首次初始化，它仍会以错误退出：它无法记录初始化已完成。锁空闲后，再运行同一条命令即可。

:::warning 回收无法中断已经开始的步骤
检查发生在步骤*之间*。如果“应用基础设施”才运行一秒时发生回收，也必须等该步骤返回才能停止；基础设施应用可能持续数十分钟。因此风险窗口是当前步骤剩余的时间，而不是十秒检测间隔。这才是回收采用缓慢、手动、输入确认方式而不自动执行的实际原因。
:::

反方向也有同样的保护。如果一个运行在完整租约期限内一直**无法续约**，例如发生网络分区或无法访问 API 服务器，它会认为自己已失去锁并停止，不会一边继续自信地应用变更，一边让别人合理地判断它已经退出。

## 中断运行 {#interrupt}

`Ctrl+C` 会正常停止运行。`dcctl` 会要求基础设施工具按自己的方式停止：完成正在进行的操作并写入状态文件。随后 `dcctl` 会归还锁再退出，让下一次运行（通常就是你的重试）发现集群空闲。CI 运行器或调度器用于取消任务的 `SIGTERM`，与第一次 `Ctrl+C` 的处理相同。

一次应用中的单个 Helm 发布，超时可能以分钟计，所以正常停止允许耗时较长：最坏可达二十分钟，通常远低于这个数值。

**第二次 `Ctrl+C` 会立即退出。** 当你判断继续等待正常停止已不值得时，可以使用这个逃生机制。

:::warning 第二次中断会放弃两种保护
它不等正常停止就结束进程，因此基础设施工具可能在应用到一半时被杀死，无法记录刚创建的资源。同时锁也**不会**归还，下一次针对该集群的运行必须等完一个租约期限并[回收](#reclaim)锁。只有第一次中断没有进展时才使用第二次中断，不要把它当作正常停止方式。
:::

### 正常停止一直不返回时 {#abandoned}

还有第三种结果。无人监控的运行，例如 CI 或计划任务，最容易遇到它，因为没有人能再次按下 `Ctrl+C`。

OpenTofu 自身会响应停止请求。但它启动的某个进程，通常是 provider 插件，可能在 OpenTofu 退出后继续运行，仍占用 `dcctl` 用来读取输出的管道，让 `dcctl` 始终无法观察到命令结束。为了避免无限等待，`dcctl` 会在**二十分钟预算之后约一分钟**主动放弃，并以错误退出。日志中会出现：

```
dcctl stopped waiting for the interrupted OpenTofu command. OpenTofu itself has gone,
but something it started — a provider plugin, most likely — outlived it and is still
holding the pipe dcctl reads its output from, so dcctl cannot see the command end. It
was asked to stop gracefully and to write its state before this point and very probably
did, but nothing here witnessed that: treat this instance's infrastructure as PARTIALLY
APPLIED rather than untouched. Re-run the same command — the apply is idempotent and
reconciles whatever was left half done. Assuming nothing happened is the one reading
that is not safe (dcctl waited 21m0s after the interrupt)
```

由此有两个结果，恰好都与第二次中断相反：

- **锁会归还。** 这是步骤失败，不是强制终止，所以会像其他错误一样释放锁。无需[回收](#reclaim)，下一次运行会发现集群空闲。
- **不能认定基础设施未被修改。** OpenTofu 被要求写入状态，而且很可能已经写入，但 `dcctl` 没有观察到这一点。重新运行原命令（`bootstrap`、`upgrade` 或 `destroy`），让应用流程协调未完成的部分。唯一不安全的解读是“什么也没发生”。

计时只从中断时刻开始。没有被中断的应用会一直等待，直到完成。

## 另请参阅 {#see-also}

- [初始化实例](./bootstrap.md)——运行中每一步的作用。
- [部署与Operator](./kubernetes-operator.md#instance-declaration)——运行写入的实例声明、保护它的 finalizer，以及 `dcctl instances release`。
