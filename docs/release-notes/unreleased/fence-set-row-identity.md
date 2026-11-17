---
section: whats-new
---

## en

New fence-set snapshots include an optional `fenceId` matching `GeoFence.id`, so deleting and recreating the same token can be distinguished even when its geometry is unchanged. The snapshot GraphQL entry exposes this as nullable `fenceId: ID`. Older snapshots, including those minted by older pods during a rolling upgrade, return null: identity is never inferred from today's token. History is not rewritten. The next fence mutation handled by a new pod captures identity in a new version, even if only presentation fields changed. No migration or operator action is required.

## es

Las nuevas instantáneas de geocercas incluyen un campo opcional `fenceId` que coincide con `GeoFence.id`, para distinguir la eliminación y recreación del mismo token aunque la geometría no cambie. La entrada GraphQL lo expone como `fenceId: ID` nullable. Las instantáneas anteriores, incluidas las creadas por pods antiguos durante una actualización gradual, devuelven null: la identidad nunca se deduce del token actual. No se reescribe el historial. La siguiente modificación procesada por un pod nuevo captura la identidad en una nueva versión, incluso si solo cambian campos de presentación. No requiere migración ni intervención del operador.

## zh-CN

新的围栏集快照包含可选的 `fenceId`，与 `GeoFence.id` 一致，因此即使几何形状不变，也能区分删除后使用相同令牌重新创建的围栏。GraphQL 快照条目将其公开为可为空的 `fenceId: ID`。旧快照（包括滚动升级期间由旧 Pod 创建的快照）返回 null；服务不会根据当前令牌推断历史身份。历史记录不会被改写。新 Pod 处理下一次围栏修改时，会在新版本中记录身份，即使只修改了展示字段。无需迁移或操作员干预。
