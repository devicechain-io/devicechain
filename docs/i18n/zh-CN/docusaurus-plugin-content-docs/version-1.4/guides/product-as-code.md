---
sidebar_position: 16
title: 产品即代码
---

# 产品即代码 {#product-as-code}

你可以把租户的产品保存在 git 中。`dcctl product export` 会把租户的设备类型、设备配置文件、仪表板、出站连接器和入站集成写成一个文件目录。`dcctl product plan` 显示应用这些文件会改变什么，`dcctl product apply` 则让租户与这些文件一致。可用它把产品从开发租户推进到生产租户、在拉取请求中评审变更，或发现租户偏离了已提交的内容。

:::caution 预览
命令树和包格式（`apiVersion: product.devicechain.io/v1alpha1`）目前为预览。它们是新的，后续的次要版本可能会更改格式。`dcctl` 只读取它认识的 `apiVersion`，拒绝其他版本，因此包不会被只理解一半。
:::

这些命令与控制台使用相同的租户 GraphQL API，并以你登录的身份执行。服务器会对每次调用授权，因此你需要具备控制台进行同样更改所需的权限。

## 导出、规划、应用 {#workflow}

```
dcctl product export ./acme --tenant acme --email you@example.com --password '<password>'
git add acme && git commit -m "Snapshot the acme product"

dcctl product plan  ./acme --tenant acme-prod --email you@example.com --password '<password>'
dcctl product apply ./acme --tenant acme-prod --email you@example.com --password '<password>'
```

`export` 写入的目录必须为空、不存在，或本身已是一个包（含有 `product.yaml`）。重新导出到已有的包会重写其文件，并删除已不存在的资源对应的文件。在两次导出之间没有任何变化时，两次写出的字节完全相同，因此 `git diff` 只显示真实的变更。用 `--name` 可在 `product.yaml` 中为产品加上标签。

`plan` 读取租户并为每项变更打印一行，不写入任何内容：

```
+ DeviceProfile    thermostat  create
~ Metric           thermostat/m-temp-c  update: unit
- Command          thermostat/c-old  delete (not in the bundle)
^ DeviceProfile    thermostat  publish (the draft changed)
~ Dashboard        overview  update: definition
5 change(s).
```

每行以 `+`（创建）、`~`（更新）、`-`（删除）或 `^`（发布）开头。对于已经包含该包的租户，`plan` 打印 `No changes`。加上 `--fail-on-changes` 后，只要有任何变更就以非零状态退出，从而让计划成为 CI 中的偏移检查。

`apply` 构建同样的计划，打印并执行它。传入 `--yes` 可跳过先打印计划。它按令牌执行 upsert，因此运行两次与运行一次相同，中途失败的运行只需再次运行即可完成。

用 `--kinds` 可把命令限定在 `device-types`、`profiles`、`integrations`、`connectors` 和 `dashboards` 中的部分类型。不带该参数时，在未运行出站连接器功能区的实例上，`export` 会跳过连接器并给出警告；`plan` 和 `apply` 只读取包中含有的类型。

## 包中有什么 {#layout}

```
product.yaml                 清单：apiVersion、kind，以及可选的 name
device-types/<token>.yaml
profiles/<token>.yaml        一个配置文件及其全部定义
integrations/<token>.yaml
connectors/<token>.yaml
dashboards/<token>.json
```

每个文件都以其内部的令牌命名，并带有 `kind`。读取是严格的：未知字段、未知目录、文件名不是其令牌，或令牌重复，都会产生指明文件的错误。键按字母顺序写出，文件内的每个 JSON 文档都以规范形式写出（键已排序、没有多余空白），与服务器返回的格式无关。

### 设备类型

```yaml
kind: DeviceType
token: thermo-v1
name: Thermo v1
profile: thermostat          # 该类型采用的配置文件的令牌
manufacturer: Acme
model: T1
maintenanceWindow: night     # 一个引用，见“包中没有什么”
metadata: {sku: T-1}
```

### 配置文件

配置文件对应的文件包含该配置文件及其上创建的全部内容。

```yaml
kind: DeviceProfile
token: thermostat
name: Thermostat
category: hvac
location: {expectedAccuracyMeters: 5.5, expectedUpdateIntervalSeconds: 60}
configurationDeclaration:
  - {key: setpoint, valueType: DOUBLE}
decoder:                      # 载荷解码器文档，包括其测试向量
  kind: CEL
  envVersion: 1
  input: FRAME
  vectors: [...]
metrics:
  - {token: m-temp-c, key: temp-c, dataType: DOUBLE, unit: Cel, min: -40, max: 85}
calculatedMetrics:
  - {token: m-temp-f, key: temp-f, dataType: DOUBLE, expression: temp-c * 9 / 5 + 32}
commands:
  - token: c-reboot
    key: reboot
    impact: DISRUPTIVE
    parameterSchema: [{name: delay, dataType: INT}]
rules:
  - token: r-overheat
    enabled: true
    definition: {...}         # 检测规则文档
    authoringGraph: {...}     # 创建它时使用的可视化画布（如果有）
```

定义的令牌（`metrics`、`calculatedMetrics`、`commands`、`rules`）在整个租户范围内唯一：一个令牌无论位于何处都指向同一个定义。指标的 `key` 是测量值使用的键，在配置文件内唯一。

### 仪表板

仪表板文件是 JSON。其定义就是控制台编辑的同一份不透明文档。

```json
{
  "kind": "Dashboard",
  "token": "overview",
  "name": "Overview",
  "definition": { "widgets": [], "layout": { "cols": 12 } }
}
```

### 连接器与集成

```yaml
kind: Connector
token: audit-sink
type: mqtt
config: {urls: ["tcp://broker.example.com:1883"], topic: alerts, qos: 1}
credential:
  handle: env:DC_CONNECTOR_AUDIT_SINK
```

```yaml
kind: Integration
token: ttn
type: TTS_V3
enabled: true
identity: DEV_EUI
deviceTypes: [thermo-v1]
linkMetrics: true
```

## 机密是句柄 {#secrets}

包中永远不含机密的值，这一点从三个方面得到保证。

- 平台不会透露已存储的连接器凭据，只告知是否已设置；集成的持有者令牌也只以摘要形式保存。导出时没有可写的内容。
- 连接器用**句柄**来指代凭据，而不是值：`env:NAME` 表示“运行 `apply` 时的环境变量 `NAME`”。对于持有凭据的连接器，`export` 会写入 `env:DC_CONNECTOR_<TOKEN>`。任何其他方案都会被拒绝。
- 每个包都会被扫描是否含有形似机密的值：位于 `password`、`secret` 或 `apiKey` 等键下的值、私钥块、云访问密钥 ID、Web 令牌、持有者凭据，或带密码的 URL。若有发现，`export` 拒绝写入，`plan` 和 `apply` 拒绝读取。消息会指明文件和规则，绝不打印该值。对于你确认不是机密的值，可在 `plan` 和 `apply` 上使用 `--allow-suspected-secrets` 跳过扫描。

`apply` 只会把凭据发送给尚未设置凭据的连接器。平台无法告诉你存储了什么，因此无法判断变量是否已更改；传入 `--rotate-secrets` 可把凭据发送给所有已设置变量的连接器。需要凭据而其变量未设置的连接器会阻塞计划。从连接器文件中删除 `credential` 会删除已存储的凭据。

入站集成的持有者令牌由服务器在创建集成时生成。`apply` 不会请求它，因此它从不会被接收、打印或保存。请在控制台中轮换该集成的令牌以签发一个。

## apply 如何收敛 {#apply}

Apply 按依赖顺序运行：连接器、含其定义的配置文件、设备类型、集成，最后是仪表板。在配置文件内部，它先删除包中不再列出的定义，然后更新，再创建，因此被删除腾出的键可以重新使用。

- **先草稿，后发布。** 配置文件、连接器或仪表板先作为草稿收敛，然后发布。只有在内容发生变化或资源从未发布过时，Apply 才会发布。对于配置文件，当活动版本的指标、命令、解码器或配置声明与包不同时，也会发布。已发布的版本无法回读其规则或位置声明，因此在 `apply` 之外对规则或位置所做的更改本身不会触发发布。连接器在其草稿的写入时间晚于最新版本时发布，仪表板在草稿与已发布版本不同时发布。`--no-publish` 只收敛草稿并停止，`--republish` 则会发布包中的每个配置文件、连接器和仪表板，无论是否看起来有变化：可用它来完成在草稿写入后被跳过或失败的发布。发布的版本带有标签 `dcctl product apply`。
- **只处理包中指明的内容。** 位于租户中但不在包里的设备类型、配置文件、集成、连接器或仪表板不会被改动，`plan` 会把它们列为说明。唯一的例外是包所含配置文件下的定义，它们会收敛到包中的内容：`apply` 会删除未列出的草稿指标、命令或规则。
- **移动。** 若某个定义的令牌应归于另一个配置文件，则通过更新来移动，而不是删除后重建。
- **无法更改的内容。** 集成的类型，以及指标是测量型还是计算型，在创建时就已固定。包与租户在这两点上不一致即为阻塞项，`apply` 会在写入任何内容之前拒绝整个计划。

## 并发编辑 {#concurrency}

| 类型 | 应用期间的保护 |
| --- | --- |
| 连接器 | 更新和发布以制定计划时读取的版本戳为条件。在此期间被他人修改的连接器会被以 `modified by another writer` 拒绝，而不会被覆盖。 |
| 仪表板 | 相同：更新和发布都带有版本戳。 |
| 设备类型、配置文件及其定义、集成 | 无。它们没有版本戳，因此并发编辑遵循“最后写入者胜出”。 |

请让 `plan` 和 `apply` 的执行时间靠近，并在被拒绝后重新运行 `plan`。

## 包中没有什么 {#limits}

- 维护窗口、动态分组、客户、地理围栏、资产、设备、凭据和通知渠道不属于包。设备类型的 `maintenanceWindow` 和规则的 `entityGroupToken` 是按令牌的引用；你应用到的租户必须已包含它们，否则服务器会拒绝写入。
- 仪表板与客户和分组的共享关系不包含在内。
- `apply` 从不删除整个设备类型、配置文件、集成、连接器或仪表板。
- `export` 写出的是草稿。从未发布的草稿同样会被导出，`apply` 会在另一端发布它。
