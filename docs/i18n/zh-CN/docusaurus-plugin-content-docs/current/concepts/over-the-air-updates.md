---
title: 空中（OTA）更新
---

# 空中（OTA）更新 {#over-the-air-updates}

**`update-management`** 服务向设备下发固件、软件和配置镜像。它按租户维护**制品**目录，允许你将制品**分配**给设备，向设备发送获取镜像所需的信息，并跟踪每次**更新尝试**，直到设备证明自己正在运行新版本。

:::note 状态
**目前可用：**带校验的流式上传制品目录、一次分配给一台设备、按设备组分批分配制品的[发布](#rollouts)（限制同时更新的设备数，支持暂停、恢复、中止和失败阈值）、通过短期授权进行可续传的下载，以及带截止期的尝试跟踪。控制台提供制品、签名者和发布页面，每台设备还有一个“更新”选项卡，用于分配和跟踪更新；参见用户指南中的[更新](/user-guide/updates)。仓库中还有一台小型参考设备，完整实现了协议的设备端；参见[构建可更新的设备](../guides/building-an-updatable-device.md)。

**尚未提供：**发布还没有分批（waves）、金丝雀晋级、计划或维护窗口。只有通过 MQTT 连接的设备才能接收托管更新，因为分配通过设备的 MQTT 通道传递。只使用 HTTP 的设备不能接收；[LwM2M](./lwm2m.md) 设备改为通过普通命令更新；[Sparkplug](./sparkplug.md) 没有固件更新；参见[哪些设备可以更新](#transports)。
:::

## 启用 {#enabling}

`update-management` 属于 `default` 配置档（因此也属于 `full`）；只有 `telemetry` 和 `ingest-only` 不包含它，可在 `enabledFunctionalAreas` 中列出。它依赖 `device-management`，并将制品内容保存在[对象存储](./object-storage.md)中：未配置存储时，声明制品会以 `OBJECT_STORE_NOT_CONFIGURED` 被拒绝。在 S3 后端上，上传内容会缓冲在内存中，因此无论配置的上限是多少，制品在该后端上都限制为 64 MiB。

### 第一次更新之前 {#first-update}

设备要能接收更新，必须满足四个条件。前两项与实例有关，第三项是你只需做一次的决定，第四项关乎设备本身。

1. **实例有对象存储。**可以是多个服务都能挂载的文件系统卷，或 S3。参见[没有对象存储的实例](#no-object-store)。
2. **可以向设备发放下载链接。**Chart 会根据实例的 Ingress 主机推导出链接的基址，因此只要实例有 Ingress 就满足；参见下文的 `downloadBaseUrl`。不满足时，控制台会在“更新”页面显示提示。
3. **已注册签名者，且制品已由它签名。**默认情况下（`allowUnverifiedArtifacts: false`），没有人签名的制品不能被分配：分配会以 `UNVERIFIED_ARTIFACT_REFUSED` 被拒绝。注册签名者需要 `artifact:trust` 权限和你自己生成的公钥；请按[签名更新](../guides/signing-updates.md)操作。开启 `allowUnverifiedArtifacts` 会取消这一要求，仅适用于开发实例。
4. **设备通过 MQTT 使用该协议**，并持有签名者的公钥。参见[哪些设备可以更新](#transports)和[构建可更新的设备](../guides/building-an-updatable-device.md)。

有两项设置是需要刻意决定的，而不是可以放任不管的默认值。两者都位于 Chart values 的 `functionalAreas.update-management.config.updates` 下：

| 设置 | 默认值 | 含义 |
| --- | --- | --- |
| `allowUnverifiedArtifacts` | `false` | 设为 `false` 时，只有由处于活动状态的[签名者](#signers)签名的制品才能被分配。请在开发或测试实例上设为 `true`，以便也能分配未签名的制品。该值会显示在 `updateManagementInfo` 查询中，从而可以看出实例启用了它。 |
| `downloadBaseUrl` | 由 Ingress 主机推导 | 设备访问该服务的公共 URL，不含查询参数或片段。未设置时，Chart 会将其设为 `<公共来源>/api/update-management`，其中来源是 Ingress 主机：Ingress TLS 开启时用 `https`，否则用 `http`。你设置的键优先，将其设为空字符串则关闭下载。没有 Ingress 时无从推导，因此不设置任何值：无法向任何设备发放下载授权，设备的请求会被拒绝并计数。请在 Chart values 中设置；Instance 的调优覆盖层会拒绝该键，因为它随实例主机而定。主机不是回环地址时，`http` 基址可以启动，但服务会记录警告，而且设备会拒绝 `http` 下载，除非明确告知它可以。 |

其余设置用于限制服务：

| 设置 | 默认值 | 含义 |
| --- | --- | --- |
| `maxArtifactBytes` | 512 MiB | 单个制品的最大大小，不得超过 `maxTenantArtifactBytes`。 |
| `maxTenantArtifactBytes` | 2 GiB | 一个租户全部制品的总大小上限。 |
| `maxConcurrentUploads` | 4 | 单个副本同时处理的上传数。 |
| `maxConcurrentDownloads` | 16 | 单个副本同时提供的下载数。 |
| `capabilityTtlSeconds` | 900 | 下载授权的有效期。十五分钟同时也是上限；更大的值会在启动时被拒绝。 |
| `sweepIntervalSeconds` | 30 | 平台查找设备已失去响应的尝试的间隔。 |
| `deadlines.*` | 见[下文](#deadlines) | 平台在尝试的每个阶段等待多久。 |

负值、未知键，或者相对路径、包含凭据或查询参数的 `downloadBaseUrl`，都会在启动时被拒绝。

### 没有对象存储的实例 {#no-object-store}

制品内容需要对象存储，而存储在创建实例时就已确定（参见[对象存储](../deployment/bootstrap.md#object-store)）。有一种情况需要留意。`default` 配置档包含 `update-management`，因此在该配置档扩充之前创建的实例会在升级时获得这项服务。如果它的存储是 `dcctl` 未创建的集群上的 `ReadWriteOnce` 文件系统卷，该卷只能被一个服务挂载，而其他服务已经占用了它，于是 `dcctl` 不会拒绝升级，而是让 `update-management` 以 `updates.objectStore` 设为 `none` 的方式启动。另一个取值是默认的 `instance`。只有这一种情况下 `dcctl` 才会设为 `none`：你亲自指定的区域、`full` 配置档、可共享的卷、S3，以及 `dcctl` 自己创建的集群都不受影响。调优覆盖层不能设置该键；仅使用 Chart 的安装可以使用这两个值中的任何一个，其他值会使渲染失败。

在该模式下服务照常运行，不需要内容的功能仍然可用，例如签名者。声明制品和上传内容会以 `OBJECT_STORE_NOT_CONFIGURED` 被拒绝，而不是写入服务根本没有的卷。`dcctl` 在应用变更时会说明这一点，`updateManagementInfo` 的 `objectStoreConfigured` 为 `false`，控制台也会在“更新”页面显示提示。

在没有存储的情况下启动的实例，无法通过升级获得存储。要在其中使用更新，请重新创建实例，使用 `--object-store s3`，或使用文件系统存储并在支持的存储类上指定 `--object-store-access-mode ReadWriteMany`。

## 制品 {#artifacts}

制品先声明、不带内容：`createArtifact` 接收令牌、**类型**（`FIRMWARE`、`SOFTWARE` 或 `CONFIGURATION`）、名称、版本标签、要更新的设备**部件**（例如 `firmware` 或 `app/agent`）、精确的字节大小、SHA-256 摘要，以及与之**兼容**的设备配置文件（至少一个，且每个都必须存在）。名称、部件和版本三者组合在租户内唯一。在存储任何内容之前，会先按两个上限检查声明的大小。

制品的初始状态为 `UPLOADING`。随后通过 HTTP `PUT` 将内容发送到 `/api/update-management/updates/artifacts/{token}/content`，并使用具有 `artifact:write` 权限的租户访问令牌进行认证；该路由所需的 Ingress 设置见 [GraphQL API 参考](../reference/graphql-api.md#endpoints)。上传以流式进行，并按实际到达的内容判定：比声明大小更长或更短，或哈希与摘要不符的内容会被丢弃，制品保持 `UPLOADING`。只有内容相符时制品才会变为 **`AVAILABLE`**，此后不可变。`retireArtifact` 使其不再用于新的分配；其记录和内容会保留。

## 分配更新 {#assigning}

`assignArtifact(deviceToken, artifactToken)` 让一台设备安装一个 `AVAILABLE` 制品。设备的配置文件必须在制品的兼容列表中，除非制品已由处于活动状态的签名者[签名](#signers)，或 `allowUnverifiedArtifacts` 已开启，否则分配会被拒绝（`UNVERIFIED_ARTIFACT_REFUSED`）。对[已停用的设备](../guides/device-credentials.md#disabling-a-device)分配会以 `DEVICE_DISABLED` 被拒绝；设备被停用期间，它的更新报告和下载请求会被丢弃，它已获得的下载链接也会失效，直到它重新启用，届时会重新向它提供其当前的分配。停用不会取消正在进行的更新：[截止期](#deadlines)照常计时，可能把更新转为 `TIMED_OUT` 或 `UNKNOWN`；如果设备在该阶段截止期之前重新启用，它的下一次报告会继续该更新，它已获得的下载链接也会重新生效。设备**每个部件只有一个目标**：

- 该部件的上一次更新仍在进行时，新的分配会被拒绝，直到上一次更新被取消；
- 在该更新仍在进行时再次分配同一个制品，会返回已有的分配；
- 上一次更新结束后再分配，会**取代**它，历史记录会保留。

**分配会带上它所依据的签名。**制品已签名时，分配会选取其上最早的、签名者仍处于活动状态的那份签名，并将它**固定**到该分配上。设备收到的消息包含 `trust: SIGNED`，以及这份签名（标准 base64）、签名者的 `keyId` 和 `algorithm`（`ECDSA_P256_SHA256` 或 `ED25519`），设备据此用自己信任的密钥进行校验。分配上的 `signedBy` 给出它所固定的签名者。如果分配时没有任何处于活动状态的签名者的签名（只有在开启 `allowUnverifiedArtifacts` 时才可能），则不固定任何签名，线上仍为 `UNVERIFIED`。固定的签名不会改变：此后即使签名者被吊销，已在进行中的更新仍沿用分配时的签名，每次重发的分配消息都完全相同；而对该制品的*新*分配会被拒绝。如果平台无法读取所固定的签名，它宁可什么都不发送，也不会把该分配当作 `UNVERIFIED` 发送。

对运维人员，这意味着两点：

- **设备必须持有所有可能被固定的签名者的密钥。**签名者仍处于活动状态的最早一份签名胜出，因此设备被告知应对照哪个签名者校验，取决于谁为该制品签过名。请在吊销旧签名者**之前**，先把新签名者的密钥放到设备上，也要早于旧签名者的签名不再是最早的活动签名。
- **关闭 `allowUnverifiedArtifacts` 会使未签名的分配不再被发送**，包括设备同步时的重发。设备保留它已有的内容；请分配已签名的制品让它继续前进。

`cancelUpdate` 会立即取消排队中的更新。在安装开始之前，它会记录取消请求，当设备确认已放弃该工作时更新变为 `CANCELLED`。设备一旦开始安装，取消就会被拒绝：安装无法撤回。

读取目录、分配和签名者需要 `artifact:read`。更改目录、分配以及附加签名需要 `artifact:write`。注册或吊销签名者需要 `artifact:trust`，它是有意分开的：能够发布二进制文件，并不包含决定谁的签名使其可信的权力。

## 向设备组发布 {#rollouts}

**发布（rollout）**把一个制品分配给某个[设备组](../user-guide/groups-facets-browse.md)中的设备，每次只处理有限数量的设备，并且可以暂停、恢复和中止。

1. `createUpdateRollout(request)` 以 `DRAFT` 状态声明发布：制品（必须为 `AVAILABLE` 且可分配，规则与 `assignArtifact` 相同）、设备组、`maxInFlight`（1 到 1000）以及可选的 `failureThreshold`。此时不会解析或发送任何内容。
2. `startUpdateRollout(token)` **冻结设备组的成员**作为本次发布的设备，并将其转为 `RUNNING`。之后加入该组的设备不属于本次发布，离开该组的设备仍是目标；要覆盖新加入的设备，请新建一次发布。动态组按其已发布版本解析，发布会记录该版本。超过 10,000 台设备的组会被拒绝（`ROLLOUT_TOO_LARGE`），而不是被截断。
3. 随后服务按顺序把制品分配给这些设备，任何时候未完成更新的设备都不超过 `maxInFlight` 台。每台设备都遵循与 `assignArtifact` 相同的规则：不存在、已停用、配置档不兼容或正忙于另一项更新的设备会以该拒绝的代码（`DEVICE_NOT_FOUND`、`DEVICE_DISABLED`、`INCOMPATIBLE_PROFILE`、`UPDATE_IN_PROGRESS`）被**跳过**，且不占用名额。已经在处理同一制品、或已经更新到该制品的设备，会按其现有分配计入，而不会再次通知。
4. 当没有设备在等待、也没有设备存在未完成的更新时，发布变为 `COMPLETED`。

设备的结果始终取自它自己的更新尝试：到达 `UPDATED` 为 `SUCCEEDED`，`FAILED` 或 `TIMED_OUT` 为 `FAILED`，结果未知时为 `UNKNOWN`。**`UNKNOWN` 的设备会继续占用** `maxInFlight` 中的名额，并计为失败，因为它可能已经安装了更新却没有再上报。

| 操作 | 作用 | 不会做的事 |
| --- | --- | --- |
| `pauseUpdateRollout` | 不再通知更多设备更新。 | 已在更新的设备会继续。 |
| `resumeUpdateRollout` | 继续派发。 | 不会清零失败计数：因阈值暂停后，除非传入更高的 `failureThreshold`，否则会被拒绝（`ROLLOUT_FAILURE_THRESHOLD_REACHED`）。 |
| `abortUpdateRollout` | 尚未通知的设备变为 `CANCELLED`，发布永久结束。 | 已在更新的设备会被留下完成更新，并继续显示。可用 `cancelUpdate` 取消其中任意一台。 |

当失败和未知设备的数量达到 `failureThreshold` 时，发布会以原因 `FAILURE_THRESHOLD` 自行暂停。如果在运行期间制品被停用，或失去本实例要求的签名，发布也会以 `ARTIFACT_NOT_ASSIGNABLE` 暂停。

`updateRollout(token)` 和 `updateRollouts(criteria)` 返回每次发布及其 `progress`（待处理、进行中、成功、失败、未知、已取消和已跳过设备的数量），`updateRolloutTargets(token, criteria)` 分页列出其设备及结果，并可按结果筛选。发布所需的权限与单台分配相同：读取需要 `artifact:read`，创建和控制需要 `artifact:write`。仅限部分客户的成员无法查看或控制发布，因为一次发布涵盖该成员范围之外的设备。每一次开始、暂停、恢复和中止都会以操作者的名义记入[审计日志](../user-guide/audit.md)。

发布在服务重启后以及服务有多个副本时都会继续运行：每个副本每隔几秒推进正在运行的发布，而同一次发布同一时间只由一个副本推进，因此无论有多少副本，上限都成立。发布的分批（waves）、计划和维护窗口目前尚不可用。

## 尝试如何推进 {#states}

每个分配带有一次**尝试**，平台根据设备的报告推导其状态：

| 状态 | 含义 |
| --- | --- |
| `QUEUED` | 已创建；设备尚未报告。 |
| `INITIATED` | 设备已确认收到分配。 |
| `DOWNLOADING` | 设备正在下载制品，如有报告则附带字节数和总数。 |
| `DOWNLOADED` | 设备已持有全部字节。 |
| `VERIFIED` | 设备所持字节的哈希与制品摘要一致。 |
| `UPDATING` | 设备正在安装。 |
| `REBOOTING` | 设备正在重启进入新版本。 |
| `UPDATED` | 设备正在运行目标版本。 |
| `FAILED` | 设备报告了失败；或者对于 `UNKNOWN` 的更新，其清单报告显示设备已重启但运行的不是目标版本。 |
| `TIMED_OUT` | 在报告开始安装之前超过了某个截止期。 |
| `UNKNOWN` | 在安装或重启期间超过了某个截止期。 |
| `CANCELLED` | 更新在安装开始前被取消，或运维人员放弃了一个 `UNKNOWN` 更新。 |

设备若因签名而拒绝某个分配，会报告 `FAILED`：设备不信任签名者的密钥，或设备要求签名而分配是 `UNVERIFIED` 时，代码为 `UNKNOWN_SIGNER`；签名无法通过校验时，代码为 `SIGNATURE_INVALID`。设备要对照已持有的字节来校验签名，所以错误或未知的签名要在 `DOWNLOADED` 之后、算出哈希时才会报告。要求签名镜像的设备可以更早拒绝 `UNVERIFIED` 分配：在 `RECEIVED` 之后、下载任何内容之前，参考设备就是这样做的。两种方式都有效，因为设备可以跳过某个阶段。

报告只会让尝试向前推进；较早阶段的报告会被忽略。**只有凭证据才能到达 `UPDATED`**：设备报告目标版本正在运行，并且当制品需要重启时（所有 `FIRMWARE` 制品都需要；`SOFTWARE` 和 `CONFIGURATION` 制品不需要），该报告来自与安装时不同的一次启动。确认收到或安装程序自身的“成功”都不算证据。

### 截止期 {#deadlines}

截止期从平台最后接受的一次报告开始计算，因此持续报告的慢速设备不会超时。每一项都是 `updates.deadlines` 下的一个键，单位为秒：

| 键 | 覆盖的状态 | 默认值 | 超时后 |
| --- | --- | --- | --- |
| `acknowledgeSeconds` | `QUEUED`、`INITIATED` | 1 天 | `TIMED_OUT` |
| `downloadSeconds` | `DOWNLOADING`、`DOWNLOADED` | 6 小时 | `TIMED_OUT` |
| `verifySeconds` | `VERIFIED` | 30 分钟 | `TIMED_OUT` |
| `installSeconds` | `UPDATING` | 1 小时 | `UNKNOWN` |
| `confirmSeconds` | `REBOOTING` | 1 小时 | `UNKNOWN` |

安装或重启期间的沉默是 **`UNKNOWN`**，既不是失败也不是成功：设备可能处于任一版本。`UNKNOWN` 尝试只有凭证据才能离开该状态。设备的下一次清单报告会说明它在运行什么，尝试随之变为 `UPDATED`，或者在设备已重启但不在目标版本时变为 `FAILED`。在此之前，该部件的新分配会被拒绝。如果设备永远不会报告，`abandonUnknownUpdate(assignmentToken, reason)` 会将更新以 `CANCELLED` 结束，保存你的原因，以你的名义记录变更，通知设备并释放该部件。它不会查明设备实际运行什么，因此在依赖任一版本之前请先确认。

## 签名者 {#signers}

制品的**信任状态**表明是否有你信任的一方为它担保。在你注册的至少一个**签名者**为它签名之前，它是 `UNVERIFIED`；签名之后为 `SIGNED`。该状态在每次读取时根据已记录的签名及其签名者的状态计算得出，因此 `artifacts` 可以按 `trustStatus` 过滤。

签名者就是一个公钥。`registerArtifactSigner(name, publicKeyPem)` 接收一个包含 ECDSA P-256 或 Ed25519 公钥的 SPKI `PUBLIC KEY` PEM 块；其他密钥类型和曲线会被拒绝。只注册公钥这一半：平台从不需要私钥，收到私钥会被拒绝（`SIGNER_KEY_REFUSED`），并且不会回显、不会写入日志、也不会保存。签名者的 `keyId` 是密钥 DER 编码的 SHA-256 小写十六进制，同一个密钥在一个租户内只能注册一次。

你用自己保管的私钥对制品的 **SHA-256 摘要**签名，对象是 32 个原始字节，而不是十六进制文本：ECDSA P-256 生成摘要的 ASN.1 DER 签名，Ed25519 则对这 32 个摘要字节签名。`addArtifactSignature(artifactToken, signerToken, signature)` 接收标准 base64 编码的签名，并用平台在声明制品时保存的摘要来校验。无法通过校验的签名会被拒绝（`SIGNATURE_INVALID`），因此针对其他字节生成的签名无法附加；已吊销的签名者会被拒绝（`SIGNER_REVOKED`）。同一签名者重复签名不会产生任何变化。签名只覆盖摘要：它不说明镜像对应哪个版本、部件或兼容的配置文件，所以以另一个版本声明的同一镜像，同样由这个签名担保。设备必须自行强制检查版本和部件；参见[设备端](#device-side)。

`revokeArtifactSigner(token)` 不可撤销。该签名者的签名仍保留在记录中，但不再计入，因此仅由它担保的制品会立即变为 `UNVERIFIED`，不能再被分配（除非开启了 `allowUnverifiedArtifacts`）；不会改写任何制品。签名者的 `soleSignedArtifactCount` 会预先告诉你这会影响多少制品。已经做出的分配不会被收回。

## “已校验”的含义 {#verified}

`VERIFIED` 以及上传时的大小与摘要检查，确立的是**完整性**：字节与声明的一致。它们不说明这些字节由谁生成；这正是[签名](#signers)补充的内容。已签名制品的分配会告知设备 `SIGNED` 并附上签名，因此持有签名者公钥的设备可以在安装前确认镜像是真实的。两种签名算法签名的都是 SHA-256 摘要的 32 个原始字节，而不是文件本身。

## 哪些设备可以更新 {#transports}

托管更新是设备 MQTT 通道上的一次对话：平台在 `device-desired` 上发布分配和下载链接，设备在 `device-reports` 上回应。只有下载本身走 HTTP。这决定了平台能更新哪些设备。

| 设备 | 托管更新 | 替代做法 |
| --- | --- | --- |
| 通过 MQTT 连接 | 可以 | 参见[连接设备](../guides/connecting-a-device.md#over-the-air-updates)。 |
| 只使用 HTTP | 不可以。它可以通过 HTTP 发送事件，但没有任何途径告知它存在分配，它也没有可供报告的通道。 | 同时让它通过 MQTT 连接，或用你自己的方式更新。 |
| [LwM2M](./lwm2m.md) | 不可以。平台不会送达 LwM2M 设备。 | 用普通命令驱动标准 Firmware Update 对象，参见[使用对象 5 更新固件](./lwm2m.md#firmware-update-object-5)。该运行手册是受支持的途径，但不是托管任务：没有制品、没有签名转发、也没有尝试跟踪。 |
| [Sparkplug](./sparkplug.md) 边缘节点 | 不可以。Sparkplug 没有定义固件更新，DeviceChain 也不另行发明。 | 需要通过 DeviceChain 更新的节点，同时以普通 MQTT 设备的身份连接 DeviceChain，并使用此通道。参见[更新边缘节点](./sparkplug.md#updating-an-edge-node)。 |

## 设备端 {#device-side}

设备在其 `device-desired` 主题上得知分配，申请下载授权，通过 HTTP 以可续传的 `Range` 请求获取镜像，并在 `device-reports` 上报告每个阶段。消息格式见[连接设备](../guides/connecting-a-device.md#over-the-air-updates)。

平台在字节到达时按摘要校验它们，并把针对该摘要的签名交给设备，由设备做决定。由于签名只覆盖摘要，一个签名正确的旧镜像，或为另一个部件构建的镜像，仍然是有效的已签名镜像。平台不会把版本或部件绑定到签名上，因此设备必须拒绝不比当前运行版本更新的版本，也必须拒绝不属于自己部件的镜像。设备还必须在摘要和签名校验完成之前不运行镜像的任何部分，把信任密钥保存在本地而不是从消息中取用，并且只有在有证据表明目标版本确实在运行时才报告 `RUNNING`。[构建可更新的设备](../guides/building-an-updatable-device.md)演示了一个完成上述全部检查的参考实现，并给出检查的顺序。

## 监控 {#monitoring}

该服务的指标（包括报告结论、下载授权以及被截止期移动的尝试）列在[指标参考](../deployment/metrics-reference.md#update-management)中。

## 相关内容 {#related}

- **[构建可更新的设备](../guides/building-an-updatable-device.md)**：以参考设备演示设备端的实现。
- **[签名更新](../guides/signing-updates.md)**：创建密钥并为制品签名。
- **[对象存储](./object-storage.md)**：制品内容的保存位置。
- **[设备配置](../guides/connecting-a-device.md#reserved-device-topics)**：通过相同设备通道下发的单设备设置。
- **[GraphQL API](../reference/graphql-api.md#endpoints)**：各端点，包括上传和下载路由。
