---
section: before-upgrade
---

## en

- **Device bulk changes (Preview).** In v1.10, device “fleet operations” are now called “bulk changes”. Use GraphQL `bulkChangeReadiness` (`BulkChangeReadiness`, `BulkChangeEnforcement`) and `dcctl bulk-change readiness`. The deprecated `fleetOperationReadiness` field, its `FleetOperationReadiness` / `FleetOperationEnforcement` types, and `dcctl fleet-op readiness` keep working, including for v1.9 clients. The old command prints a one-line deprecation notice to stderr. The `fleet:operate` authority is unchanged. The Preview clock still starts at v1.9.

## es

- **Cambios masivos de dispositivos (Preview).** En v1.10, las “operaciones de flota” de dispositivos pasan a llamarse “cambios masivos”. Utiliza GraphQL `bulkChangeReadiness` (`BulkChangeReadiness`, `BulkChangeEnforcement`) y `dcctl bulk-change readiness`. La consulta obsoleta `fleetOperationReadiness`, sus tipos `FleetOperationReadiness` / `FleetOperationEnforcement` y `dcctl fleet-op readiness` siguen funcionando, incluidos los clientes v1.9. El comando antiguo escribe un aviso de obsolescencia de una línea en stderr. El permiso `fleet:operate` no cambia. El plazo de vista previa sigue comenzando en v1.9.

## zh-CN

- **设备批量变更（Preview）。** 从 v1.10 起，设备“群操作”称为“批量变更”。请使用 GraphQL `bulkChangeReadiness`（`BulkChangeReadiness`、`BulkChangeEnforcement`）和 `dcctl bulk-change readiness`。已弃用的 `fleetOperationReadiness` 字段、其 `FleetOperationReadiness` / `FleetOperationEnforcement` 类型以及 `dcctl fleet-op readiness` 仍可使用，包括 v1.9 客户端。旧命令会向 stderr 输出一行弃用提示。`fleet:operate` 权限保持不变。预览期限仍从 v1.9 开始。
