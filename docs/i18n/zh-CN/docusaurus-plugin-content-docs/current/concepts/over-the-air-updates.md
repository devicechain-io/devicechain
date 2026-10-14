---
title: 空中（OTA）更新
---

# 空中（OTA）更新 {#over-the-air-updates}

可选启用的 **`update-management`** 服务向设备下发固件、软件和配置镜像。它按租户维护**制品**目录，允许你将制品**分配**给设备，向设备发送获取镜像所需的信息，并跟踪每次**更新尝试**，直到设备证明自己正在运行新版本。

:::note 状态
**目前可用：**带校验的流式上传制品目录、一次分配给一台设备、通过短期授权进行可续传的下载，以及带截止期的尝试跟踪。

**尚未提供：**没有面向整个设备群的发布（分阶段或按百分比分配给大量设备）；更新没有控制台界面，需通过 GraphQL 和 HTTP 上传来使用；只使用 HTTP 的设备无法接收更新，因为分配通过设备的 MQTT 通道传递。出于同样的原因，[LwM2M](./lwm2m.md) 和 [Sparkplug](./sparkplug.md) 设备也无法接收更新；LwM2M 设备的固件改为通过普通命令驱动。
:::

## 启用 {#enabling}

`update-management` 不在 `default` 配置档中。使用 `--profile full` 安装，或在 `enabledFunctionalAreas` 中列出它。它依赖 `device-management`，并将制品内容保存在[对象存储](./object-storage.md)中：未配置存储时，声明制品会以 `OBJECT_STORE_NOT_CONFIGURED` 被拒绝。在 S3 后端上，上传内容会缓冲在内存中，因此无论配置的上限是多少，制品在该后端上都限制为 64 MiB。

有两项设置是需要刻意决定的，而不是可以放任不管的默认值。两者都位于 Chart values 的 `functionalAreas.update-management.config.updates` 下：

| 设置 | 默认值 | 含义 |
| --- | --- | --- |
| `allowUnverifiedArtifacts` | `false` | 设为 `false` 时，只有由处于活动状态的[签名者](#signers)签名的制品才能被分配。请在开发或测试实例上设为 `true`，以便也能分配未签名的制品。该值会显示在 `updateManagementInfo` 查询中，从而可以看出实例启用了它。 |
| `downloadBaseUrl` | 未设置 | 设备访问该服务的公共 URL，例如 `https://<ingress host>/api/update-management`，不含查询参数或片段。未设置时无法向任何设备发放下载授权：设备的请求会被拒绝并计数。 |

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

## 制品 {#artifacts}

制品先声明、不带内容：`createArtifact` 接收令牌、**类型**（`FIRMWARE`、`SOFTWARE` 或 `CONFIGURATION`）、名称、版本标签、要更新的设备**部件**（例如 `firmware` 或 `app/agent`）、精确的字节大小、SHA-256 摘要，以及与之**兼容**的设备配置文件（至少一个，且每个都必须存在）。名称、部件和版本三者组合在租户内唯一。在存储任何内容之前，会先按两个上限检查声明的大小。

制品的初始状态为 `UPLOADING`。随后通过 HTTP `PUT` 将内容发送到 `/api/update-management/updates/artifacts/{token}/content`，并使用具有 `artifact:write` 权限的租户访问令牌进行认证；该路由所需的 Ingress 设置见 [GraphQL API 参考](../reference/graphql-api.md#endpoints)。上传以流式进行，并按实际到达的内容判定：比声明大小更长或更短，或哈希与摘要不符的内容会被丢弃，制品保持 `UPLOADING`。只有内容相符时制品才会变为 **`AVAILABLE`**，此后不可变。`retireArtifact` 使其不再用于新的分配；其记录和内容会保留。

## 分配更新 {#assigning}

`assignArtifact(deviceToken, artifactToken)` 让一台设备安装一个 `AVAILABLE` 制品。设备的配置文件必须在制品的兼容列表中，除非制品已由处于活动状态的签名者[签名](#signers)，或 `allowUnverifiedArtifacts` 已开启，否则分配会被拒绝（`UNVERIFIED_ARTIFACT_REFUSED`）。对[已停用的设备](../guides/device-credentials.md#disabling-a-device)分配会以 `DEVICE_DISABLED` 被拒绝；设备被停用期间，它的更新报告和下载请求会被丢弃，它已获得的下载链接也会失效，直到它重新启用，届时会重新向它提供其当前的分配。停用不会取消正在进行的更新：[截止期](#deadlines)照常计时，可能把更新转为 `TIMED_OUT` 或 `UNKNOWN`；如果设备在该阶段截止期之前重新启用，它的下一次报告会继续该更新，它已获得的下载链接也会重新生效。设备**每个部件只有一个目标**：

- 该部件的上一次更新仍在进行时，新的分配会被拒绝，直到上一次更新被取消；
- 在该更新仍在进行时再次分配同一个制品，会返回已有的分配；
- 上一次更新结束后再分配，会**取代**它，历史记录会保留。

**分配会带上它所依据的签名。**制品已签名时，分配会选取其上最早的、签名者仍处于活动状态的那份签名，并将它**固定**到该分配上。设备收到的消息包含 `trust: SIGNED`，以及这份签名（标准 base64）、签名者的 `keyId` 和 `algorithm`（`ECDSA_P256_SHA256` 或 `ED25519`），设备据此用自己信任的密钥进行校验。分配上的 `signedBy` 给出它所固定的签名者。如果分配时没有任何处于活动状态的签名者的签名（只有在开启 `allowUnverifiedArtifacts` 时才可能），则不固定任何签名，线上仍为 `UNVERIFIED`。固定的签名不会改变：此后即使签名者被吊销，已在进行中的更新仍沿用分配时的签名，每次重发的分配消息都完全相同；而对该制品的*新*分配会被拒绝。如果平台无法读取所固定的签名，它宁可什么都不发送，也不会把该分配当作 `UNVERIFIED` 发送。

`cancelUpdate` 会立即取消排队中的更新。在安装开始之前，它会记录取消请求，当设备确认已放弃该工作时更新变为 `CANCELLED`。设备一旦开始安装，取消就会被拒绝：安装无法撤回。

读取目录、分配和签名者需要 `artifact:read`。更改目录、分配以及附加签名需要 `artifact:write`。注册或吊销签名者需要 `artifact:trust`，它是有意分开的：能够发布二进制文件，并不包含决定谁的签名使其可信的权力。

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

设备若因签名而拒绝某个分配，会在 `DOWNLOADED` 阶段（已持有字节并算出哈希之后）报告 `FAILED`：设备不信任签名者的密钥，或设备要求签名而分配是 `UNVERIFIED` 时，代码为 `UNKNOWN_SIGNER`；签名无法通过校验时，代码为 `SIGNATURE_INVALID`。

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

你用自己保管的私钥对制品的 **SHA-256 摘要**签名，对象是 32 个原始字节，而不是十六进制文本：ECDSA P-256 生成摘要的 ASN.1 DER 签名，Ed25519 则对这 32 个摘要字节签名。`addArtifactSignature(artifactToken, signerToken, signature)` 接收标准 base64 编码的签名，并用平台在声明制品时保存的摘要来校验。无法通过校验的签名会被拒绝（`SIGNATURE_INVALID`），因此针对其他字节生成的签名无法附加；已吊销的签名者会被拒绝（`SIGNER_REVOKED`）。同一签名者重复签名不会产生任何变化。签名只覆盖摘要：它不说明镜像对应哪个版本、部件或兼容的配置文件，所以以另一个版本声明的同一镜像，同样由这个签名担保。

`revokeArtifactSigner(token)` 不可撤销。该签名者的签名仍保留在记录中，但不再计入，因此仅由它担保的制品会立即变为 `UNVERIFIED`，不能再被分配（除非开启了 `allowUnverifiedArtifacts`）；不会改写任何制品。签名者的 `soleSignedArtifactCount` 会预先告诉你这会影响多少制品。已经做出的分配不会被收回。

## “已校验”的含义 {#verified}

`VERIFIED` 以及上传时的大小与摘要检查，确立的是**完整性**：字节与声明的一致。它们不说明这些字节由谁生成；这正是[签名](#signers)补充的内容。已签名制品的分配会告知设备 `SIGNED` 并附上签名，因此持有签名者公钥的设备可以在安装前确认镜像是真实的。两种签名算法签名的都是 SHA-256 摘要的 32 个原始字节，而不是文件本身。

## 设备端 {#device-side}

设备在其 `device-desired` 主题上得知分配，申请下载授权，通过 HTTP 以可续传的 `Range` 请求获取镜像，并在 `device-reports` 上报告每个阶段。消息格式见[连接设备](../guides/connecting-a-device.md#over-the-air-updates)。

## 监控 {#monitoring}

该服务的指标（包括报告结论、下载授权以及被截止期移动的尝试）列在[指标参考](../deployment/metrics-reference.md#update-management)中。

## 相关内容 {#related}

- **[对象存储](./object-storage.md)**：制品内容的保存位置。
- **[设备配置](../guides/connecting-a-device.md#reserved-device-topics)**：通过相同设备通道下发的单设备设置。
- **[GraphQL API](../reference/graphql-api.md#endpoints)**：各端点，包括上传和下载路由。
