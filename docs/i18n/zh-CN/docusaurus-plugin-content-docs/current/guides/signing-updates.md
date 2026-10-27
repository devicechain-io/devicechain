---
sidebar_position: 12
title: 签名更新
---

# 签名更新

摘要校验只能证明镜像就是所声明的那个镜像，不能证明它由谁制作。本指南说明如何补上这一证明：注册贵组织用于签名的公钥，对每个制品的摘要签名，再由平台把对应的签名交给设备，由设备做出最终决定。

:::note 状态
签名随更新服务中的签名者注册表一起提供。在您的实例运行包含它的版本之前，制品保持 `UNVERIFIED`，分配也不携带签名。相关概念请参阅[空中（OTA）更新](../concepts/over-the-air-updates.md#verified)；设备收到的消息请参阅[连接设备](./connecting-a-device.md#over-the-air-updates)。
:::

## 信任如何运作

- **签名者**是租户信任的一把公钥。平台只保存公钥部分，并拒绝私钥。
- **签名**针对制品的 SHA-256 摘要生成，并附加到制品上。平台会用上传时保存的摘要对其进行校验。
- 制品的**信任状态**在每次读取时推导得出：只要至少有一个活动签名者在其上有已校验的签名，即为 `SIGNED`，否则为 `UNVERIFIED`。因此撤销某个签名者会改变它所担保的所有制品，而无需写入这些制品。
- 分配 `UNVERIFIED` 制品会被拒绝，除非实例允许未校验的制品（`updates.allowUnverifiedArtifacts`）。
- **设备**使用自己已持有的密钥校验签名。平台只转发签名，从不替设备做决定。

这里涉及两种授权，刻意分开：`artifact:trust` 决定谁的签名有效（注册和撤销签名者），`artifact:write` 负责发布制品和附加签名。

## 1. 生成密钥

使用 P-256 上的 ECDSA 或 Ed25519，不接受其他密钥类型或曲线。请把私钥保存在平台之外，也不要放在不需要它的构建服务器上。

```bash
# ECDSA P-256
openssl ecparam -name prime256v1 -genkey -noout | openssl pkcs8 -topk8 -nocrypt -out signer.pem

# 或 Ed25519
openssl genpkey -algorithm ed25519 -out signer.pem

# 公钥部分，SPKI PEM 块（注册时使用）
openssl pkey -in signer.pem -pubout -out signer.pub.pem
```

## 2. 计算密钥 ID

密钥的标识，即设备据以查找的 `keyId`，是其公钥（DER 格式的 `SubjectPublicKeyInfo`）的 SHA-256，使用小写十六进制：

```bash
openssl pkey -in signer.pem -pubout -outform DER | openssl dgst -sha256 -r | cut -d' ' -f1
```

注册密钥时平台会计算相同的值，并拒绝对同一把密钥的第二次注册。签名者的令牌是 `signer-` 加上密钥 ID 的前 16 个十六进制字符。

## 3. 注册签名者

需要 `artifact:trust`。只传公钥 PEM。

```graphql
mutation {
  registerArtifactSigner(
    name: "Release signing key 2026"
    publicKeyPem: "-----BEGIN PUBLIC KEY-----\nMFkw…\n-----END PUBLIC KEY-----\n"
  ) {
    token
    keyId
    algorithm
    state
  }
}
```

私钥、其他密钥类型或曲线，以及已注册的密钥都会被拒绝。`artifactSigners` 列出租户的签名者及其状态，每个签名者还会报告 `soleSignedArtifactCount`，即撤销它后会退回 `UNVERIFIED` 的制品数量。

## 4. 对制品摘要签名

两种算法签名的都是制品的**原始 32 字节 SHA-256 摘要**。请对您上传的字节签名；结果必须能通过制品声明时所用 `digest` 的校验。

**ECDSA P-256。** `openssl dgst -sha256 -sign` 会先对文件求哈希再对摘要签名，生成平台和设备所期望的 ASN.1 DER 签名：

```bash
openssl dgst -sha256 -sign signer.pem -binary firmware.bin | base64 | tr -d '\n'
```

据 `cosign sign-blob --key` 的文档，它对 ECDSA 密钥生成相同形式的签名，因此其 base64 输出应当也能被接受。该行为来自 cosign 的文档，本指南并未实测；在依赖它之前，请先用一个临时制品确认。

**Ed25519。** Ed25519 直接对消息签名，所以消息必须是 32 字节的摘要，而不是文件：

```bash
openssl dgst -sha256 -binary firmware.bin > digest
openssl pkeyutl -sign -rawin -inkey signer.pem -in digest | base64 | tr -d '\n'
```

:::warning 不要用 Ed25519 对文件签名
`openssl pkeyutl -sign -rawin -in firmware.bin`（以及直接对 blob 签名的工具）签名的是文件本身。这样的签名在密码学上有效，但仍然是错的：平台和设备都针对摘要校验，所以它在附加时会被拒绝，或在安装时被设备拒绝。请始终按上文方式对 `digest` 文件签名。
:::

这些方法已用 OpenSSL 3.2.1 对照一个独立的校验程序验证过：该程序计算文件的 SHA-256，并调用 Go 标准库的 ECDSA 与 Ed25519 校验。上面两种签名都能通过校验，而对文件而非摘要所做的 Ed25519 签名不能。

## 5. 附加签名

需要 `artifact:write`。平台在保留签名之前，会用制品已存储的摘要对其校验。

```graphql
mutation {
  addArtifactSignature(
    artifactToken: "fw-1-4-2"
    signerToken: "signer-b1b9eef5a4de45e2"
    signature: "MEQCIHGf…"
  ) {
    token
    trustStatus
  }
}
```

无法通过校验的签名会被拒绝，已撤销签名者的签名也一样。同一签名者再次签名不会产生变化。一个制品可以带有多个签名者的签名。分配时，平台会固定活动签名者中**最早**的那个签名，并把该签名连同其 `keyId` 和算法（`ECDSA_P256_SHA256` 或 `ED25519`）发送给设备。

制品的 `signatures` 字段按从早到晚的顺序列出已记录的签名。每个条目包含 `signer`、`createdAt`、`createdBy` 和 `countsTowardTrust`；签名者处于活动状态时 `countsTowardTrust` 为 true，签名者被撤销后为 false。已撤销签名者的签名仍会列出。签名字节永远不会返回。控制台的制品页面在 **Signatures and trust** 下显示同样的列表。

## 6. 把密钥放到设备上

平台不管理设备端的信任。每台应接受您更新的设备，都必须在收到已签名的分配之前持有该公钥（至少是其 `keyId` 和密钥）。对于它不持有的 `keyId`，设备以 `UNKNOWN_SIGNER` 使更新失败；对于无法通过校验的签名，则以 `SIGNATURE_INVALID` 失败。请通过出厂镜像，或通过您已校验过的配置更新来下发密钥。

设备必须持有它可能收到的分配中**所有可能被固定的签名者**的密钥，而不只是您最新的密钥。

## 轮换密钥

1. 生成并注册新的签名者。
2. 把新公钥下发到所有设备，并等待整个设备群都已持有。**在**用它签任何东西**之前**完成这一步。
3. 用新密钥为新制品签名。若一个制品同时带有两个签名，固定的是最早的活动签名，因此旧密钥会一直被使用，直到被撤销。
4. 当没有设备再需要旧签名者时，撤销它。

在新密钥到达设备之前，不要撤销旧密钥。否则尚未收到新密钥的设备会收到它们无法校验的签名。

## 撤销签名者

需要 `artifact:trust`。

```graphql
mutation {
  revokeArtifactSigner(token: "signer-b1b9eef5a4de45e2") { token state revokedAt }
}
```

撤销是终态且幂等的。该签名者的签名仍保留在记录中，但不再计数：

- 仅由它担保的制品立即变为 `UNVERIFIED`，对这些制品的新分配会被拒绝，除非实例允许未校验的制品。
- 已固定到该签名的分配保持不变，因此进行中的更新会按分配时的样子送达。
- 若平台无法读取某个分配所固定的签名，它什么都不发送，而不是把更新以未签名方式发出。
- 实例的该开关关闭时，未固定签名的分配（在允许未校验制品期间创建的）不会被发送。

撤销前，请先查看 `soleSignedArtifactCount`，了解撤销会影响哪些制品。

## 签名不涵盖什么 {#what-a-signature-does-not-cover}

签名**只绑定摘要**，不绑定制品的版本、组件或兼容的配置文件。任何拥有 `artifact:write` 的人都可以把先前已签名的镜像以新版本重新声明，旧签名仍然能通过校验。如果需要拒绝降级或装错组件，必须由设备自己强制执行，并且发布权限应与签名权限一样谨慎持有。[构建可更新的设备](./building-an-updatable-device.md#refusing-downgrades)演示了参考设备如何做到这一点。

## 相关

- **[签名者](../concepts/over-the-air-updates.md#signers)**：签名如何使制品受信任，以及吊销签名者会改变什么。
- **[空中（OTA）更新](../concepts/over-the-air-updates.md#assigning)**：分配更新，以及一次尝试如何推进。
- **[连接设备](./connecting-a-device.md#over-the-air-updates)**：分配消息和设备端的校验。
- **[构建可更新的设备](./building-an-updatable-device.md)**：一台会校验、安装并回滚的参考设备。
