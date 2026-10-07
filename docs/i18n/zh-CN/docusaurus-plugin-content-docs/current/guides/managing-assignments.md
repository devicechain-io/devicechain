---
sidebar_position: 4
title: 管理设备分配
---

# 管理设备分配 {#managing-device-assignments}

**分配**将设备关联到客户、区域或资产，让遥测数据携带组织上下文。在 DeviceChain 中，分配是统一实体图上的**被跟踪关系**，没有独立的分配记录。

:::note 状态
已提供。通过控制台设备详情页的**分配（Assignment）**选项卡，或 device-management GraphQL API 管理分配。
:::

## 分配用于组织，而非准入控制 {#assignment-organizes-it-does-not-gate}

设备通过凭据认证。分配只组织数据，两者相互独立：

- 已注册且有凭据的设备可以立即上报遥测，即使没有分配。事件会解析为空锚点集合：仍然持久化并更新设备实时状态，只是尚未归属于客户、区域或资产。
- 之后分配设备，会为后续事件提供锚点（`device-management` 运行多个副本时，大约五秒内生效），从而可以通过“7 号楼的所有读数”等查询找到这些事件。

因此，未分配设备绝不会被静默丢弃。这与早期行为有所不同。

## 每项分配都是锚点 {#every-assignment-is-an-anchor}

一台设备可以同时有多项分配：客户、区域和资产。设备上报事件时，每项分配都作为**锚点**记录到该事件。同一读数可以按每个维度查询，在客户和区域下都会出现。没有哪项分配是主要分配，它们地位相同。

每个事件的锚点保存在配套的 `event_anchors` 集合中，每项分配一行。按锚点筛选的查询（“区域 Y 的事件”）匹配锚点集合包含该锚点的事件。

锚点在写入时捕获，因此历史数据稳定。设备之后移动到其他区域时，每个旧事件仍保留事件发生时设备所在的区域。

## 分配设备（控制台） {#assign-a-device-console}

1. 打开设备详情页，选择**分配（Assignment）**选项卡。
2. 选择目标类型（客户 / 区域 / 资产），再选择目标实体。
3. 点击**分配（Assign）**。
4. 重复操作添加更多分配。设备可以同时分配到多个目标。

取消分配时，点击对应行的**取消分配（Unassign）**。设备未来的事件不再锚定到该目标，已记录事件则保留其锚点。

## 分配设备（GraphQL） {#assign-a-device-graphql}

分配是保留类型 **`assigned`** 的关系边。这是内置的被跟踪类型，每个租户首次使用时会自动创建。通过批量变更创建，使用 `(type, token)` 寻址源和目标：

```graphql
mutation {
  createEntityRelationships(requests: [{
    token: "3f1c…",            # a fresh unique edge token (e.g. a UUID)
    sourceType: "device",
    source: "sensor-001",       # device token
    targetType: "customer",     # customer | area | asset
    target: "lucidworks",       # target entity token
    relationshipType: "assigned"
  }]) { id token }
}
```

通过查询设备上 `assigned` 类型的被跟踪关系边，列出设备分配：

```graphql
query {
  entityRelationships(criteria: {
    sourceType: "device", source: "sensor-001",
    relationshipType: "assigned", pageNumber: 1, pageSize: 100
  }) {
    results { id token targetType target { token } }
  }
}
```

使用 `removeEntityRelationships(tokens: ["<edge token>"])` 删除一项。

创建和移除分配需要 `device:write` 权限，列出分配需要 `device:read`。

## 关系与分配 {#relationship-vs-assignment}

分配是通用关系图的一种用途。同样的 `createEntityRelationships` 和 `removeEntityRelationships` 变更操作还支持组成员关系（保留的非跟踪类型 `member`），以及你定义的任意自定义关系类型。关系之所以成为锚定事件的分配，是因为其类型为**被跟踪**。参见[领域模型](../concepts/domain-model.md#relationships)。
