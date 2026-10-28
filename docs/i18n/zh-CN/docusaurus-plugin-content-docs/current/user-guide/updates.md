---
title: 更新（空中升级）
sidebar_position: 16
---

import GuideShot from '@site/src/components/GuideShot';

# 更新（空中升级）{#updates-over-the-air}

**Updates**（更新）区域用于通过空中（OTA）方式向设备交付新的固件、软件或配置。你上传更新镜像（**制品**），用你信任的密钥生成的签名为其担保（**签名者**），然后将它分配给设备。设备随后下载并安装，控制台会跟踪每一步，直到设备证明它正在运行新版本。

本章面向租户管理员以及发布设备软件的人员，介绍控制台界面；更新如何端到端运作，请参阅[空中更新](/concepts/over-the-air-updates)，创建密钥和为制品签名请参阅[为更新签名](/guides/signing-updates)。

更新服务是可选的，并非每个安装都运行它。如果你的实例没有运行，侧边栏中就没有 **Updates** 组，设备也没有 Updates 选项卡。参见[启用](/concepts/over-the-air-updates#enabling)。

侧边栏中，**Updates** 有三个界面：**Artifacts**（制品）、**Rollouts**（发布）和 **Signers**（签名者）。更新整个设备组在 [Rollouts](#rollouts) 上进行。为设备分配更新在设备自己的页面上进行，即其 [Updates 选项卡](#assigning-an-update-to-a-device)。

## 制品 {#artifacts}

制品是一个更新镜像：一个文件，加上它是什么（类型、名称、版本和部件）以及与哪些设备配置文件兼容。内容上传后，制品不可更改。

<GuideShot id="updates-artifacts-list" alt="Artifacts 列表，包含三个温控器固件镜像：2.5.0-rc.1，Unverified 且 Available；2.4.0，Signed 且 Available；以及 2.3.1，Signed 且 Retired。每行显示部件、大小和 SHA-256 摘要的开头部分。顶部有筛选器和“New artifact”按钮。" />

每一行显示名称、版本、部件、**Trust**（信任：Signed 或 Unverified）、**State**（状态）、大小和 SHA-256 **Digest**（摘要）。状态有：

- **Uploading**（上传中）：制品已声明，但内容尚未存储。
- **Available**（可用）：内容已存储并通过校验，因此制品可以分配。
- **Retired**（已停用）：制品已不再用于新的分配。其元数据和内容保留。

**Any trust**（任意信任状态）和 **Any state**（任意状态）选择器会筛选整个目录。**Filter this page…**（筛选本页）框则不同：它只缩小已加载页面的范围，按名称、版本、部件、令牌或摘要匹配。要查看更多，请使用 **Next**（下一页）。

### 添加制品 {#adding-an-artifact}

点击 **New artifact**（新建制品）。你从文件开始，其余由控制台一步完成：在浏览器中计算文件的大小和 SHA-256，声明制品，并上传内容。

<GuideShot id="updates-artifact-new" alt="New artifact 抽屉。有“Choose file”按钮并附说明 Largest accepted: 512 MiB，然后是 Kind、Name、Version、Component、Token、Compatible device profiles 和 Media type 字段。" />

1. 点击 **Choose file**（选择文件）。按钮下方显示你的实例接受的最大文件；更大的文件在发送任何内容之前就会被拒绝。
2. 选择 **Kind**（类型）：Firmware、Software 或 Configuration。
3. 输入 **Name**（名称）、**Version**（版本）和 **Component**（部件）。版本是平台不解析的标签。部件是此更新所针对的设备部分，例如 `firmware` 或 `app/agent`。名称、部件和版本的组合必须唯一。
4. **Token**（令牌）会根据名称和版本自动填入，你可以修改。
5. 选择 **Compatible device profiles**（兼容的设备配置文件）。制品只能分配给使用这些配置文件的设备，因此至少选择一个。
6. 可选：设置 **Media type**（媒体类型，留空则为 `application/octet-stream`）、**Source**（来源）和 **Notes**（备注）。
7. 点击 **Declare and upload**（声明并上传）。

上传会显示进度。完成后，控制台会把服务存储的内容与你声明的内容进行比较。**Content stored and verified**（内容已存储并通过校验）表示两者一致。如果不一致，控制台会提示，制品保持不可用：请检查文件并重新上传。

如果制品声明之后上传失败，该制品存在但没有内容。点击 **Retry upload**（重试上传），或 **Open artifact**（打开制品）并在其页面上上传文件。

### 制品页面 {#the-artifact-page}

打开制品即可看到它的页面。页眉显示名称和版本、令牌，以及其信任状态、状态和类型的徽章。**Retire**（停用）在你确认后将制品从新的分配中撤下。停用无法撤销。

<GuideShot id="updates-artifact-signed" alt="Thermostat firmware 2.4.0 的页面，带有 Signed、Available 和 FIRMWARE 徽章以及 Retire 按钮。Details 面板列出部件、版本、大小、媒体类型、摘要、声明时间和最后更改时间、兼容的配置文件 bp-thermostat-profile，以及记录的 notes、source 和 uploadedBy 条目。其下的 Signatures and trust 面板显示 Signed 徽章和一个来自 Acme release signing key 的签名。" />

**Details**（详情）面板列出已声明的内容，完整摘要可通过复制按钮获取。随制品记录的任何来源信息（例如备注和来源）随后列出。这些条目按其存储时的键显示，不做翻译（例如 **UPLOADEDBY**）。

仍处于 **Uploading** 状态的制品有一个 **Upload content**（上传内容）面板。选择为该制品声明的文件；控制台会在发送任何内容之前检查其大小和 SHA-256，并拒绝不是所声明的文件。

### 签名与信任 {#signatures-and-trust}

制品的信任状态在每次读取时，根据已记录的签名以及其背后签名者的状态计算得出：

- **Signed**（已签名）：至少有一个处于活动状态的签名者对该制品有已验证的签名。
- **Unverified**（未验证）：没有任何处于活动状态的签名者对其有已验证的签名。平台仍会检查字节与声明的字节一致，但不说明它们由谁制作。

**Signatures and trust**（签名与信任）面板显示信任状态，然后按从旧到新的顺序列出此制品上记录的签名。对每个签名，你会看到签名者的名称、其密钥 ID、算法、签名者的状态、签名的添加时间和添加者，以及它是否仍然 **Counts toward trust**（计入信任）。来自此后已被吊销的签名者的签名仍保留记录，但显示为 **No longer counts (signer revoked)**（不再计入，签名者已吊销）。没有签名的制品会说明这一点。

<GuideShot id="updates-artifact-signatures" alt="Thermostat firmware 2.4.0 页面的下半部分：Signatures and trust 面板带有 Signed 徽章，表格中有一个来自 Acme release signing key 的签名：其密钥 ID、ED25519 算法、Active 状态、添加时间和添加者，以及 Counts toward trust。其下是带有 Signer key ID 和 Signature (base64) 字段的 Add a signature 表单。" />

要添加签名，请填写面板底部的 **Add a signature**（添加签名）：

- **Signer key ID**（签名者密钥 ID）：已注册签名者的密钥 ID，如 Signers 界面所示或由你的签名工具打印出来。控制台会告诉你它匹配哪个签名者，或在签名者已被吊销或找不到时发出警告。
- **Signature (base64)**（签名，base64）：对制品摘要的签名。Ed25519 密钥对 32 字节的原始摘要签名；ECDSA P-256 签名与 `cosign sign-blob` 相符。

点击 **Add signature**（添加签名）。服务会针对制品存储的摘要验证签名，并拒绝验证不通过的签名。每一步的命令见[签名指南](/guides/signing-updates)。

### 未验证的制品 {#unverified-artifacts}

未验证的制品在其页面顶部带有一条通知，说明你的实例允许什么：通常，未验证的制品在签名之前**不能分配**给设备。实例可以被设置为允许分配，这仅用于开发。如果控制台无法读取该设置，通知会告诉你应假定该制品不能分配。

<GuideShot id="updates-artifact-unverified" alt="Thermostat firmware 2.5.0-rc.1 的页面，带有 Unverified、Available 和 FIRMWARE 徽章。一条通知写道：This artifact is unverified. This instance does not allow unverified artifacts to be assigned, so this one cannot be assigned to a device until it is signed. 其中链接到签名指南。" />

## 签名者 {#signers}

**签名者**是你的租户信任的、可为制品担保的公钥。只存储公钥；私钥保留在签名者手中。打开 **Updates → Signers**。

<GuideShot id="updates-signers-list" alt="Signers 列表，有两个签名者：Acme release signing key，ED25519，Active，有 2 个仅由它签名的制品和 Revoke 按钮；以及 Contract manufacturer key (2025)，ECDSA_P256，Revoked，附有吊销时间和 0 个仅由它签名的制品。" />

列表显示每个签名者的名称、密钥 ID、算法、状态、注册和吊销时间，以及其 **Sole-signed artifacts**（唯一签名的制品）：没有其他活动签名者的制品数量，因此如果吊销该签名者，这些制品会变为未验证。

注册和吊销签名者需要 `artifact:trust` 权限，它与上传制品的权限是分开的。没有该权限时，你能看到列表，但没有任何操作。

### 注册签名者 {#registering-a-signer}

点击 **Register signer**（注册签名者）并输入：

- **Name**（名称）：用于识别签名者的标签，例如持有该密钥的团队或构建流水线。
- **Public key (PEM)**（公钥，PEM）：一个 SPKI 公钥块，ECDSA P-256 或 Ed25519。

<GuideShot id="updates-signer-register" alt="Register signer 抽屉，包含 Name 字段、显示 BEGIN 和 END PUBLIC KEY 行的 Public key (PEM) 字段、一个关于如何生成密钥并计算其密钥 ID 的链接，以及 Register 按钮。" />

点击 **Register**（注册）。只粘贴密钥对的公钥部分。如果你粘贴的内容看起来像私钥，控制台不会发送任何内容，并会清空该字段。抽屉中的链接指向[签名指南](/guides/signing-updates)，其中介绍了如何生成密钥并计算密钥 ID。

### 吊销签名者 {#revoking-a-signer}

在处于活动状态的签名者上点击 **Revoke**（吊销）。确认框会告诉你有多少制品没有其他活动签名者，并将变为 **UNVERIFIED**。该数量包含已停用的制品。

<GuideShot id="updates-signer-revoke" alt="确认对话框“Revoke Acme release signing key”。内容说明有 2 个制品没有其他活动签名者，将变为 UNVERIFIED，并且已发出的更新分配会保留其固定时所用的签名。有 Cancel 和“Revoke signer”按钮。" />

吊销是永久性的。该签名者的签名保留记录但不再计入，因此仅由它担保的制品会立即变为未验证。已经分配的更新保留分配时的签名，因此仍按分配时的情况交付。参见[签名指南](/guides/signing-updates)中的“Revoking a signer”。

## 为设备分配更新 {#assigning-an-update-to-a-device}

更新逐台分配，在设备的页面上进行。打开设备并选择 **Updates** 选项卡。只有当你的实例运行更新服务，且你有权读取更新时，该选项卡才会出现。

<GuideShot id="updates-device-tab" alt="Lobby thermostat 设备的 Updates 选项卡。在 Current update 下，Thermostat firmware 2.4.0 为 Signed 和 QUEUED，附有部件、尝试 ID、进度 Not reported、上次报告时间、分配者、Needs reboot Yes，以及固定的签名者 Acme release signing key（ACTIVE）。随后是“Cancel update”按钮。下方的 Assign an update 部分有 Artifact 选择器、一条设备已有更新正在进行的说明，以及禁用的 Assign 按钮。" />

顶部的链接指向签名指南和签名者的工作方式。**Refresh**（刷新）重新加载该选项卡。

### 当前更新 {#current-update}

**Current update**（当前更新）卡片显示设备正在处理的更新：制品及其信任状态、更新尝试的状态、部件、尝试 ID、进度、设备上次报告的时间、分配者，以及是否需要重启。**Pinned signer**（固定的签名者）显示随更新一起发给设备的签名。如果该签名者之后被吊销，卡片会提示：更新保留分配时所用的签名。

状态徽章跟随设备的报告，从 **QUEUED** 经过下载、验证、安装和重启，直到 **UPDATED**。更新只有在有证据时才会到达 **UPDATED**：设备报告新版本正在运行。完整的状态列表见[尝试如何推进](/concepts/over-the-air-updates#states)。

**QUEUED** 表示平台已创建该更新，但设备尚未报告任何关于它的信息。在设备报告之前，进度显示 **Not reported**（未报告）。在截图中，更新一直处于 QUEUED，因为这个演示租户中的模拟温控器从不报告更新进度。接收到分配的真实设备会确认并推进它。

如果更新失败，卡片会显示原因：失败所处的阶段，是设备报告的失败还是平台判定的失败，以及对于常见的签名和下载失败，应该怎么做。

### 取消或放弃更新 {#cancelling-or-abandoning-an-update}

- 在安装开始之前都可以使用 **Cancel update**（取消更新）。处于 **QUEUED** 的更新会立即取消，之后可以分配新的更新。设备已接手更新后，取消会向它发送停止请求：只有设备确认后取消才完成，在此之前卡片显示 **Cancel pending**（取消待确认）。安装一旦开始，更新就不能再取消。
- 当更新处于未知状态时提供 **Abandon update**（放弃更新）：设备在安装或重启期间停止了报告，没有人知道它运行的是哪个版本。放弃会将更新以已取消结束，保留你给出的 **Reason**（原因，必填）作为该更新的原因，以你的名义记录这次更改，向设备发送取消，并释放设备以便重新分配。它不会查明设备正在运行什么，因此在依赖任一版本之前，请先检查设备的清单。

### 分配更新 {#assigning-an-update}

使用 **Assign an update**（分配更新）：

1. 选择一个 **Artifact**（制品）。只列出可用的制品，且设备的配置文件必须在制品的兼容列表中。当你的实例不允许未验证的制品时，未验证的制品标记为 **Not assignable**（不可分配）。
2. 点击 **Assign**（分配）。

如果设备已有更新正在进行，该部分会说明，并且在那个更新被取消或完成之前，分配另一个会被拒绝。当实例不允许未验证的制品时，对未验证的制品 **Assign** 保持禁用，并显示原因。

### 分配历史 {#assignment-history}

**Assignment history**（分配历史）列出设备过去和当前的分配，包括制品、状态、任何失败、每次分配的时间和分配者。

### 被禁用的设备 {#a-disabled-device}

当设备被禁用时，选项卡会说明它已与更新隔绝。在设备重新启用之前不能分配新的更新，**Assign** 处于禁用状态。禁用设备不会召回已在进行中的更新；如果不希望它继续，请在此选项卡上取消。取消时，仍在排队的更新会立即取消；设备已经接手的更新在设备被禁用期间保持 **Cancel pending**，因为它的报告会被拒绝：取消在设备重新启用并确认时完成，否则更新会在其所处阶段的截止期以超时结束。

## 发布 {#rollouts}

**Rollouts**（发布）每次只更新少量设备，从而更新整个设备组。当一次一台太慢、一次全部又风险太大时使用它。

**New rollout**（新建发布）需要填写令牌、可选的名称、**Artifact**（制品，仅列出可用的制品）、**Device group**（设备组）、**Devices updating at once**（同时更新的设备数）以及可选的 **Failure threshold**（失败阈值）。发布以草稿形式创建，此时不会向任何设备发送内容。

在发布页面上：

- **Start**（开始）冻结设备组当前的成员作为本次发布的设备，并通知前几台设备更新。之后加入该组的设备不包括在内；离开该组的设备仍留在发布中。
- **Pause**（暂停）停止通知更多设备。已在更新的设备会继续，**Resume**（恢复）从停下的地方继续。
- **Abort**（中止）取消尚未通知的设备并结束发布。已在更新的设备会被留下完成更新；你可以在其自己的 [Updates 选项卡](#assigning-an-update-to-a-device)中取消其中任意一台。

摘要显示待处理、进行中、成功、失败、未知、已取消和已跳过的设备数量，**Devices**（设备）表列出每台设备及其结果，并可按结果筛选。被跳过的设备是按与单台分配相同的规则被拒绝的（例如已停用的设备，或正忙于另一项更新的设备），其 **Reason**（原因）列会说明原因。结果未知的设备计为失败，并继续占用 **Devices updating at once** 中的名额。

当失败和未知的设备达到 **Failure threshold** 时，发布会自行暂停。要继续，请输入更高的阈值并选择 **Resume with this threshold**（以此阈值恢复）：恢复永远不会清零计数。如果制品在运行期间被停用，发布也会自行暂停。

查看发布需要 `artifact:read`，创建和控制发布需要 `artifact:write`。

## 仅限部分客户的成员 {#members-limited-to-some-customers}

访问仅限部分客户、且持有 `artifact:read` 的成员，可以读取那些在分配更新时分配给其中某个客户的设备的更新分配。它不能分配、取消或放弃更新，也不能列出制品、签名者或发布。实例开始记录设备分配给谁之前所做的分配，不会显示给这样的成员。

## 相关内容 {#related}

- [空中更新](/concepts/over-the-air-updates)：制品、分配、更新状态、截止期和签名者。
- [为更新签名](/guides/signing-updates)：生成密钥、注册密钥、为制品签名以及轮换密钥。
- [构建可更新的设备](/guides/building-an-updatable-device)：一台会校验、安装并回滚更新的参考设备。
