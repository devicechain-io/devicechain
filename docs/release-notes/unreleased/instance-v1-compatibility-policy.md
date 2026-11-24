---
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
section: before-upgrade
---

## en

- **Instance and executor compatibility policy.** The versioning policy now commits to covering `core.devicechain.io/v1 Instance` from v2.0; v1 is Preview in v1.10. Instance `v1beta1` stays served throughout v2.x. v2.0 will not remove `spec.grafanaSSO`, `spec.compact` or `InstanceConfiguration`. `ExecutorEnrollment` and `InstanceOperation` stay v1beta1 through v2.0; their Preview promotion is triggered by enabling action execution in a released build after the checks that refuse unsafe instance actions have shipped and explicit provider privileges are in place, rather than by the usual two-minor limit. Storage migration and executable actions have not shipped. See [Versioning and Upgrade Policy](./versioning-policy.md#instance-api-compatibility).

## es

- **Política de compatibilidad de Instance y del ejecutor.** La política de versiones se compromete ahora a cubrir `core.devicechain.io/v1 Instance` desde v2.0; v1 está en vista previa en v1.10. Instance `v1beta1` sigue ofreciéndose durante toda la serie v2.x. v2.0 no eliminará `spec.grafanaSSO`, `spec.compact` ni `InstanceConfiguration`. `ExecutorEnrollment` e `InstanceOperation` siguen en v1beta1 hasta v2.0 inclusive; su promoción desde vista previa se activa al habilitar la ejecución de acciones en una versión publicada, una vez publicadas las comprobaciones que rechazan las acciones inseguras sobre instancias y establecidos privilegios explícitos del proveedor, en lugar del límite habitual de dos versiones menores. La migración de almacenamiento y las acciones ejecutables aún no se han publicado. Consulta [Política de versiones y actualizaciones](./versioning-policy.md#instance-api-compatibility).

## zh-CN

- **Instance 与执行器兼容性策略。** 版本策略现承诺从 v2.0 起将 `core.devicechain.io/v1 Instance` 纳入保障；v1 在 v1.10 中为预览。Instance `v1beta1` 在整个 v2.x 系列继续提供服务。v2.0 不会移除 `spec.grafanaSSO`、`spec.compact` 或 `InstanceConfiguration`。`ExecutorEnrollment` 和 `InstanceOperation` 在 v2.0 及之前保持 v1beta1；它们从预览提升的触发条件是在已发布版本中启用操作执行，并且拒绝不安全实例操作的检查已发布、已具备明确的提供方权限，而非通常的两个次版本期限。存储迁移和可执行操作尚未发布。参见[版本与升级策略](./versioning-policy.md#instance-api-compatibility)。
