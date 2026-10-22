---
sidebar_position: 11
title: 验证发布版本
---

# 验证发布版本 {#verifying-releases}

DeviceChain 每个发布版本所发布的所有容器镜像和 Helm chart 都经过**签名**，并附带两份关于其构建方式的已签名声明。你可以在运行任何东西之前完成全部验证，无需账号，也无需获取任何密钥。

| 内容 | 存放位置 | 它告诉你什么 |
| --- | --- | --- |
| **签名** | 在镜像仓库中紧邻镜像或 chart | 它由 DeviceChain 的发布工作流针对该确切标签发布。 |
| **SBOM**（SPDX） | 紧邻每个镜像，作为已签名的证明 | 镜像内包含的软件包。 |
| **构建溯源**（SLSA） | 紧邻每个镜像和 chart，并记录在 GitHub 的证明存储中 | 由哪个提交、工作流和运行构建。 |

签名采用**无密钥**方式：不存在需要防止泄露或轮换的长期密钥。每个签名都绑定到生成它的工作流运行的身份，记录在公开的透明日志中，并据此身份进行验证。来自任何其他工作流、仓库或分支的签名，都不会被验证为发布版本。

:::note 适用的版本
签名适用于其发布说明中宣布了签名的版本，最迟不晚于 `v1.0.0`。更早的镜像没有签名，验证时会报错 "no signatures found"。这是正确的结果，并非你的环境有问题。
:::

## 哪些内容已签名 {#what}

- 每个服务镜像：`ghcr.io/devicechain-io/<service>`（例如 `device-management`）
- Kubernetes operator 镜像：`ghcr.io/devicechain-io/operator`
- 边缘代理镜像：`ghcr.io/devicechain-io/dc-edge-agent`
- Web 控制台镜像：`ghcr.io/devicechain-io/frontend`
- Helm chart：`oci://ghcr.io/devicechain-io/charts/devicechain`

镜像使用发布标签（`v1.0.0`）。chart 使用**不带** `v` 的版本号（`1.0.0`）。GitHub Release 附带的 `dcctl` 和边缘代理压缩包有各自的溯源信息，参见[发布与升级](./releases-and-upgrades.md)。

## 一次验证全部 {#script}

仓库提供了一个脚本，它会检查每个镜像和 chart，然后证明它自己的检查确实能够失败。它需要 [`cosign`](https://docs.sigstore.dev/cosign/system_config/installation/) 3 或更高版本、`jq`，以及已登录（`gh auth login`）的 [GitHub CLI](https://cli.github.com/)（用于溯源验证）。

```bash
curl -fsSLO https://raw.githubusercontent.com/devicechain-io/devicechain/v1.0.0/hack/verify-release-signatures.sh
chmod +x verify-release-signatures.sh
./verify-release-signatures.sh v1.0.0
```

请从你正在验证的**同一个标签**获取脚本。只要有任何签名、SBOM 或溯源检查失败，它就会以非零状态退出，并指出是哪一项。

脚本最后会运行两项**负向对照**。只见过正确输入的验证器，从未被证明能拒绝任何东西。因此脚本会用一个不可能匹配的身份去验证一个已签名的 DeviceChain 镜像，并验证一个并非 DeviceChain 发布的镜像，要求两者都因正确的原因被拒绝。跳过其中任何一项的运行都不算通过。

## 手动验证单个镜像 {#by-hand}

```bash
TAG=v1.0.0
IMAGE=ghcr.io/devicechain-io/device-management:$TAG

cosign verify "$IMAGE" \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/$TAG" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

两个参数都很重要。如果不指定身份，`cosign verify` 会接受任何能够无密钥签名的人的签名，而那是所有人。指明工作流**以及标签**，才能让验证结果表示"这是 DeviceChain 的 `$TAG` 发布"。

SBOM 与溯源：

```bash
# SBOM（已签名的证明）；打印软件包列表。
cosign verify-attestation --type spdxjson "$IMAGE" \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/$TAG" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  | jq -r '.payload | @base64d | fromjson | .predicate.packages[].name' | sort -u

# 构建溯源。
gh attestation verify "oci://$IMAGE" --repo devicechain-io/devicechain \
  --signer-workflow devicechain-io/devicechain/.github/workflows/release.yml
```

chart 的验证方式相同，只是使用它自己的标签：

```bash
cosign verify ghcr.io/devicechain-io/charts/devicechain:1.0.0 \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/v1.0.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## 按摘要固定镜像 {#digest}

标签只是一个指针，镜像仓库可以移动它。`cosign verify` 会打印它所验证的摘要；请部署**该摘要**（`image@sha256:...`），这样你运行的就是你检查过的内容。如果你把镜像镜像到自己的仓库，请连同签名一起复制（`cosign copy`），或者在镜像之前先验证并记录摘要。

若要在准入阶段而不是手动拒绝未签名镜像，可使用 [Sigstore policy controller](https://docs.sigstore.dev/policy-controller/overview/) 之类的准入策略，强制要求相同的身份和签发者。

## 这能说明什么，不能说明什么 {#limits}

- **说明来源，不说明质量。** 签名表示 DeviceChain 的发布工作流生成了该制品，并不表示代码没有缺陷；那是发布关卡和你自己的测试要解决的问题。
- **SBOM 由镜像的 `amd64` 变体生成。** `arm64` 变体由相同的源码和依赖构建，但该证明覆盖整个多架构索引，而且只来自一个平台。
- **溯源说明的是构建者，而非内容。** 它让你确认提交和工作流；它不能替代阅读 SBOM。
- **验证依赖公共的 Sigstore 基础设施**（证书颁发机构和透明日志）以及 GitHub 的身份令牌。如果你在验证所在的环境无法访问它们，请在能访问的地方完成验证，再把摘要带过去。

如需报告签名方面的问题，或此处所验证内容中的漏洞，请参阅[安全策略](https://github.com/devicechain-io/devicechain/security/policy)。
